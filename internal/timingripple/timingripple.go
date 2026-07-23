// Package timingripple calculates deterministic Forecast timing changes.
package timingripple

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strconv"
	"time"
)

// Boundary controls whether automatic timing recalculation may move an instant.
type Boundary string

const (
	// Hard prevents automatic movement.
	Hard Boundary = "Hard"
	// Soft permits automatic movement.
	Soft Boundary = "Soft"
)

// Session is the complete timing state needed by the planner.
type Session struct {
	ID              int
	PlannedStart    time.Time
	PlannedEnd      time.Time
	ForecastStart   time.Time
	ForecastEnd     time.Time
	OccupancyStart  time.Time
	OccupancyEnd    time.Time
	MinimumDuration time.Duration
	StartBoundary   Boundary
	EndBoundary     Boundary
	LaneIDs         []int
	LocationIDs     []int
}

// AdjustTarget moves one live countdown target without implicitly pulling forward.
type AdjustTarget struct {
	SessionID int
	TargetEnd time.Time
}

func (AdjustTarget) timingAction() {}

// PullForward uses an early Actual End to move eligible later Soft Boundaries.
type PullForward struct {
	SessionID int
	ActualEnd time.Time
}

func (PullForward) timingAction() {}

// Action is one supported timing recalculation request.
type Action interface {
	timingAction()
}

// Change is one Session's recalculated Forecast Time.
type Change struct {
	SessionID     int
	ForecastStart time.Time
	ForecastEnd   time.Time
}

// Effect compares one Session before and after recalculation.
type Effect struct {
	SessionID             int
	CurrentForecastStart  time.Time
	CurrentForecastEnd    time.Time
	ProposedForecastStart time.Time
	ProposedForecastEnd   time.Time
	CurrentOverlap        time.Duration
	ProposedOverlap       time.Duration
}

// HardCollision is an overlap that cannot be removed automatically.
type HardCollision struct {
	SessionID int
	Overlap   time.Duration
}

// Plan is the complete deterministic result of one timing action.
type Plan struct {
	Changes        []Change
	Effects        []Effect
	HardCollisions []HardCollision
}

// Calculate returns all Forecast changes behind one Adjust Target or Pull Forward action.
func Calculate(sessions []Session, action Action) (Plan, error) {
	graph, err := newGraph(sessions)
	if err != nil {
		return Plan{}, err
	}
	switch selected := action.(type) {
	case AdjustTarget:
		return graph.adjustTarget(selected)
	case PullForward:
		return graph.pullForward(selected)
	default:
		return Plan{}, errors.New("unsupported timing ripple action")
	}
}

// Fingerprint binds an action to the exact timing component and anchor revision.
func Fingerprint(sessions []Session, action Action, anchorRevision int) string {
	hash := sha256.New()
	writeFingerprint(hash.Write, strconv.Itoa(anchorRevision))
	switch selected := action.(type) {
	case AdjustTarget:
		writeFingerprint(hash.Write, "AdjustTarget")
		writeFingerprint(hash.Write, strconv.Itoa(selected.SessionID))
		writeFingerprint(hash.Write, selected.TargetEnd.UTC().Format(time.RFC3339Nano))
	case PullForward:
		writeFingerprint(hash.Write, "PullForward")
		writeFingerprint(hash.Write, strconv.Itoa(selected.SessionID))
		writeFingerprint(hash.Write, selected.ActualEnd.UTC().Format(time.RFC3339Nano))
	}
	ordered := slices.Clone(sessions)
	slices.SortFunc(ordered, func(first, second Session) int {
		return cmp.Compare(first.ID, second.ID)
	})
	writeFingerprint(hash.Write, "sessions")
	writeFingerprint(hash.Write, strconv.Itoa(len(ordered)))
	for _, item := range ordered {
		writeFingerprint(hash.Write, "session")
		writeFingerprint(hash.Write, strconv.Itoa(item.ID))
		writeFingerprint(hash.Write, item.PlannedStart.UTC().Format(time.RFC3339Nano))
		writeFingerprint(hash.Write, item.PlannedEnd.UTC().Format(time.RFC3339Nano))
		writeFingerprint(hash.Write, item.ForecastStart.UTC().Format(time.RFC3339Nano))
		writeFingerprint(hash.Write, item.ForecastEnd.UTC().Format(time.RFC3339Nano))
		writeFingerprint(hash.Write, item.OccupancyStart.UTC().Format(time.RFC3339Nano))
		writeFingerprint(hash.Write, item.OccupancyEnd.UTC().Format(time.RFC3339Nano))
		writeFingerprint(hash.Write, strconv.FormatInt(int64(item.MinimumDuration), 10))
		writeFingerprint(hash.Write, string(item.StartBoundary))
		writeFingerprint(hash.Write, string(item.EndBoundary))
		laneIDs := slices.Clone(item.LaneIDs)
		slices.Sort(laneIDs)
		writeFingerprint(hash.Write, "lanes")
		writeFingerprint(hash.Write, strconv.Itoa(len(laneIDs)))
		for _, laneID := range laneIDs {
			writeFingerprint(hash.Write, strconv.Itoa(laneID))
		}
		locationIDs := slices.Clone(item.LocationIDs)
		slices.Sort(locationIDs)
		writeFingerprint(hash.Write, "locations")
		writeFingerprint(hash.Write, strconv.Itoa(len(locationIDs)))
		for _, locationID := range locationIDs {
			writeFingerprint(hash.Write, strconv.Itoa(locationID))
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeFingerprint(write func([]byte) (int, error), value string) {
	_, _ = write([]byte(strconv.Itoa(len(value))))
	_, _ = write([]byte{':'})
	_, _ = write([]byte(value))
}

type graph struct {
	original     map[int]Session
	current      map[int]Session
	predecessors map[int][]int
	successors   map[int][]int
	locations    map[int][]int
	collisions   map[int]time.Duration
}

func newGraph(sessions []Session) (*graph, error) {
	result := &graph{
		original:     make(map[int]Session, len(sessions)),
		current:      make(map[int]Session, len(sessions)),
		predecessors: make(map[int][]int),
		successors:   make(map[int][]int),
		locations:    make(map[int][]int),
		collisions:   make(map[int]time.Duration),
	}
	lanes := make(map[int][]int)
	for _, item := range sessions {
		if err := validateSession(item); err != nil {
			return nil, err
		}
		if _, duplicate := result.current[item.ID]; duplicate {
			return nil, errors.New("session IDs must be unique")
		}
		item.LaneIDs = slices.Clone(item.LaneIDs)
		item.LocationIDs = slices.Clone(item.LocationIDs)
		slices.Sort(item.LaneIDs)
		slices.Sort(item.LocationIDs)
		for index := 1; index < len(item.LaneIDs); index++ {
			if item.LaneIDs[index] == item.LaneIDs[index-1] {
				return nil, errors.New("session Lane IDs must be unique")
			}
		}
		for index := 1; index < len(item.LocationIDs); index++ {
			if item.LocationIDs[index] == item.LocationIDs[index-1] {
				return nil, errors.New("session Location IDs must be unique")
			}
		}
		result.original[item.ID] = item
		result.current[item.ID] = item
		for _, laneID := range item.LaneIDs {
			lanes[laneID] = append(lanes[laneID], item.ID)
		}
		for _, locationID := range item.LocationIDs {
			result.locations[locationID] = append(result.locations[locationID], item.ID)
		}
	}
	for laneID := range lanes {
		slices.SortFunc(lanes[laneID], func(firstID, secondID int) int {
			first, second := result.original[firstID], result.original[secondID]
			if order := first.PlannedStart.Compare(second.PlannedStart); order != 0 {
				return order
			}
			return cmp.Compare(first.ID, second.ID)
		})
	}
	for _, sequence := range lanes {
		for index := 1; index < len(sequence); index++ {
			before, after := sequence[index-1], sequence[index]
			if !slices.Contains(result.predecessors[after], before) {
				result.predecessors[after] = append(result.predecessors[after], before)
			}
			if !slices.Contains(result.successors[before], after) {
				result.successors[before] = append(result.successors[before], after)
			}
		}
	}
	return result, nil
}

func validateSession(item Session) error {
	if item.ID <= 0 || item.PlannedStart.IsZero() || !item.PlannedEnd.After(item.PlannedStart) ||
		item.ForecastStart.IsZero() || !item.ForecastEnd.After(item.ForecastStart) ||
		len(item.LaneIDs) == 0 {
		return errors.New("session timing state is invalid")
	}
	if item.StartBoundary != Hard && item.StartBoundary != Soft ||
		item.EndBoundary != Hard && item.EndBoundary != Soft {
		return errors.New("session boundary is invalid")
	}
	if item.MinimumDuration < 0 || item.MinimumDuration > item.PlannedEnd.Sub(item.PlannedStart) {
		return errors.New("session Minimum Duration is invalid")
	}
	return nil
}

func (graph *graph) adjustTarget(action AdjustTarget) (Plan, error) {
	anchor, ok := graph.current[action.SessionID]
	if !ok {
		return Plan{}, errors.New("target Session is not in timing state")
	}
	if action.TargetEnd.IsZero() || !action.TargetEnd.After(anchor.ForecastStart) {
		return Plan{}, errors.New("target end must be after Forecast Start")
	}
	anchor.ForecastEnd = action.TargetEnd
	graph.current[action.SessionID] = anchor
	if action.TargetEnd.After(graph.original[action.SessionID].ForecastEnd) {
		graph.rippleDelays(action.SessionID)
	}
	return graph.plan(), nil
}

func (graph *graph) rippleDelays(anchorID int) {
	clear(graph.collisions)
	queue := slices.Clone(graph.successors[anchorID])
	for len(queue) > 0 {
		sessionID := queue[0]
		queue = queue[1:]
		current := graph.current[sessionID]
		overlap := graph.predecessorOverlap(current)
		if overlap <= 0 {
			delete(graph.collisions, sessionID)
			continue
		}
		updated, blocked := delaySession(current, overlap)
		if blocked > 0 {
			graph.collisions[sessionID] = max(graph.collisions[sessionID], blocked)
		} else {
			delete(graph.collisions, sessionID)
		}
		if !timingChanged(current, updated) {
			continue
		}
		graph.current[sessionID] = updated
		if updated.ForecastEnd.Equal(current.ForecastEnd) {
			continue
		}
		queue = append(queue, graph.successors[sessionID]...)
	}
	for sessionID, overlap := range graph.collisions {
		current := graph.current[sessionID]
		if unresolved := graph.predecessorOverlap(current); unresolved > 0 {
			graph.collisions[sessionID] = max(overlap, unresolved)
		} else {
			delete(graph.collisions, sessionID)
		}
	}
}

func timingChanged(before, after Session) bool {
	return !before.ForecastStart.Equal(after.ForecastStart) ||
		!before.ForecastEnd.Equal(after.ForecastEnd)
}

func delaySession(item Session, overlap time.Duration) (Session, time.Duration) {
	if item.StartBoundary == Hard {
		return item, overlap
	}
	compression := min(
		overlap,
		max(item.ForecastEnd.Sub(item.ForecastStart)-item.MinimumDuration, 0),
	)
	item.ForecastStart = item.ForecastStart.Add(compression)
	remaining := overlap - compression
	if remaining == 0 {
		return item, 0
	}
	if item.EndBoundary == Hard {
		return item, remaining
	}
	item.ForecastStart = item.ForecastStart.Add(remaining)
	item.ForecastEnd = item.ForecastEnd.Add(remaining)
	return item, 0
}

func (graph *graph) pullForward(action PullForward) (Plan, error) {
	anchor, ok := graph.current[action.SessionID]
	if !ok {
		return Plan{}, errors.New("ended Session is not in timing state")
	}
	if action.ActualEnd.IsZero() || action.ActualEnd.Before(anchor.ForecastStart) {
		return Plan{}, errors.New("actual End must not be before Forecast Start")
	}
	gap := anchor.ForecastEnd.Sub(action.ActualEnd)
	if gap <= 0 {
		return graph.plan(), nil
	}
	desired := make(map[int]time.Duration)
	queue := make([]int, 0)
	for _, successorID := range graph.successors[action.SessionID] {
		desired[successorID] = gap
		queue = append(queue, successorID)
	}
	for len(queue) > 0 {
		sessionID := queue[0]
		queue = queue[1:]
		original := graph.original[sessionID]
		current := graph.current[sessionID]
		if current.StartBoundary == Hard {
			continue
		}
		earliest := time.Time{}
		for _, predecessorID := range graph.predecessors[sessionID] {
			end := graph.current[predecessorID].ForecastEnd
			if predecessorID == action.SessionID {
				end = action.ActualEnd
			}
			if end.After(earliest) {
				earliest = end
			}
		}
		target := original.ForecastStart.Add(-desired[sessionID])
		if target.Before(earliest) {
			target = earliest
		}
		if !target.Before(current.ForecastStart) {
			continue
		}
		totalShift := original.ForecastStart.Sub(target)
		current.ForecastStart = target
		if current.EndBoundary == Soft {
			current.ForecastEnd = original.ForecastEnd.Add(-totalShift)
		}
		graph.current[sessionID] = current
		endShift := original.ForecastEnd.Sub(current.ForecastEnd)
		if endShift <= 0 {
			continue
		}
		for _, successorID := range graph.successors[sessionID] {
			if endShift <= desired[successorID] {
				continue
			}
			desired[successorID] = endShift
			queue = append(queue, successorID)
		}
	}
	return graph.plan(), nil
}

func (graph *graph) predecessorOverlap(item Session) time.Duration {
	return graph.overlap(item, graph.current)
}

func (graph *graph) overlap(item Session, state map[int]Session) time.Duration {
	var overlap time.Duration
	for _, predecessorID := range graph.predecessors[item.ID] {
		overlap = max(overlap, state[predecessorID].ForecastEnd.Sub(item.ForecastStart))
	}
	return overlap
}

func (graph *graph) plan() Plan {
	result := Plan{}
	currentOccupancy := graph.occupancyOverlaps(graph.original)
	proposedOccupancy := graph.occupancyOverlaps(graph.current)
	occupancyChanged := changedOccupancySessions(
		currentOccupancy.pairs, proposedOccupancy.pairs,
	)
	for sessionID, current := range graph.current {
		original := graph.original[sessionID]
		changed := timingChanged(original, current)
		currentOverlap := max(
			graph.overlap(original, graph.original),
			currentOccupancy.maximums[sessionID],
		)
		proposedOverlap := max(
			graph.overlap(current, graph.current),
			proposedOccupancy.maximums[sessionID],
		)
		if changed {
			result.Changes = append(result.Changes, Change{
				SessionID: sessionID, ForecastStart: current.ForecastStart,
				ForecastEnd: current.ForecastEnd,
			})
		}
		if changed || currentOverlap != proposedOverlap || occupancyChanged[sessionID] {
			result.Effects = append(result.Effects, Effect{
				SessionID:             sessionID,
				CurrentForecastStart:  original.ForecastStart,
				CurrentForecastEnd:    original.ForecastEnd,
				ProposedForecastStart: current.ForecastStart,
				ProposedForecastEnd:   current.ForecastEnd,
				CurrentOverlap:        currentOverlap,
				ProposedOverlap:       proposedOverlap,
			})
		}
	}
	slices.SortFunc(result.Changes, func(first, second Change) int {
		return cmp.Compare(first.SessionID, second.SessionID)
	})
	slices.SortFunc(result.Effects, func(first, second Effect) int {
		return cmp.Compare(first.SessionID, second.SessionID)
	})
	hardCollisions := maps.Clone(graph.collisions)
	for pair, sessionID := range proposedOccupancy.hardSessions {
		if currentOccupancy.pairs[pair] == proposedOccupancy.pairs[pair] {
			continue
		}
		overlap := proposedOccupancy.pairs[pair]
		hardCollisions[sessionID] = max(hardCollisions[sessionID], overlap)
	}
	for sessionID, overlap := range hardCollisions {
		result.HardCollisions = append(result.HardCollisions, HardCollision{
			SessionID: sessionID, Overlap: overlap,
		})
	}
	slices.SortFunc(result.HardCollisions, func(first, second HardCollision) int {
		return cmp.Compare(first.SessionID, second.SessionID)
	})
	return result
}

type occupancyInterval struct {
	sessionID     int
	start         time.Time
	end           time.Time
	startBoundary Boundary
}

type occupancyPair struct {
	first  int
	second int
}

type occupancyState struct {
	maximums     map[int]time.Duration
	pairs        map[occupancyPair]time.Duration
	hardSessions map[occupancyPair]int
}

func (graph *graph) occupancyOverlaps(
	state map[int]Session,
) occupancyState {
	result := occupancyState{
		maximums:     make(map[int]time.Duration),
		pairs:        make(map[occupancyPair]time.Duration),
		hardSessions: make(map[occupancyPair]int),
	}
	for _, sessionIDs := range graph.locations {
		intervals := make([]occupancyInterval, 0, len(sessionIDs))
		for _, sessionID := range sessionIDs {
			item := state[sessionID]
			start, end := item.ForecastStart, item.ForecastEnd
			if !item.OccupancyStart.IsZero() {
				start = item.OccupancyStart
			}
			if !item.OccupancyEnd.IsZero() {
				end = item.OccupancyEnd
			}
			intervals = append(intervals, occupancyInterval{
				sessionID: sessionID, start: start, end: end,
				startBoundary: item.StartBoundary,
			})
		}
		slices.SortFunc(intervals, compareOccupancyIntervals)
		active := make([]occupancyInterval, 0)
		for _, current := range intervals {
			active = slices.DeleteFunc(active, func(item occupancyInterval) bool {
				return !item.end.After(current.start)
			})
			for _, previous := range active {
				overlapEnd := current.end
				if previous.end.Before(overlapEnd) {
					overlapEnd = previous.end
				}
				overlap := overlapEnd.Sub(current.start)
				if overlap <= 0 {
					continue
				}
				result.maximums[current.sessionID] = max(
					result.maximums[current.sessionID], overlap,
				)
				result.maximums[previous.sessionID] = max(
					result.maximums[previous.sessionID], overlap,
				)
				pair := newOccupancyPair(previous.sessionID, current.sessionID)
				result.pairs[pair] = max(result.pairs[pair], overlap)
				if current.startBoundary == Hard {
					result.hardSessions[pair] = current.sessionID
				}
				if current.startBoundary != Hard &&
					previous.start.Equal(current.start) &&
					previous.startBoundary == Hard {
					result.hardSessions[pair] = previous.sessionID
				}
			}
			active = append(active, current)
		}
	}
	return result
}

func newOccupancyPair(first, second int) occupancyPair {
	if first > second {
		first, second = second, first
	}
	return occupancyPair{first: first, second: second}
}

func changedOccupancySessions(
	current map[occupancyPair]time.Duration,
	proposed map[occupancyPair]time.Duration,
) map[int]bool {
	result := make(map[int]bool)
	for pair, overlap := range current {
		if proposed[pair] == overlap {
			continue
		}
		result[pair.first] = true
		result[pair.second] = true
	}
	for pair, overlap := range proposed {
		if current[pair] == overlap {
			continue
		}
		result[pair.first] = true
		result[pair.second] = true
	}
	return result
}

func compareOccupancyIntervals(first, second occupancyInterval) int {
	if order := first.start.Compare(second.start); order != 0 {
		return order
	}
	if order := first.end.Compare(second.end); order != 0 {
		return order
	}
	return cmp.Compare(first.sessionID, second.sessionID)
}

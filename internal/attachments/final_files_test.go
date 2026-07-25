package attachments

import (
	"strings"
	"testing"

	"github.com/dotwaffle/beamers/internal/store"
)

func TestFinalFilesPathsAreGroupedSafeAndCollisionProof(t *testing.T) {
	version := store.FinalFileVersion{
		AttachmentVersion: store.AttachmentVersion{
			ID: 31, AttachmentID: 17, Version: 2, EventID: 4,
			OwnerType: store.UploadTargetEntry, OwnerID: 9,
			Name: "slides", OriginalFilename: "../../Résumé deck..PDF",
			SHA256: "abc", SizeBytes: 3,
		},
		SessionID: 6, SessionTitle: "../Demo / Final", SessionType: "Competition",
		EntryID: 9, EntryName: "Team ../../ One",
		Tracks: []store.FinalFileTrack{
			{ID: 3, Name: "Robotics / Hardware"},
			{ID: 8, Name: "Robotics / Hardware"},
		},
	}

	files := finalFilesFiles(version)
	if len(files) != 2 {
		t.Fatalf("Final Files paths = %+v", files)
	}
	for _, file := range files {
		if !strings.HasPrefix(file.Path, "tracks/Robotics-Hardware--track-") ||
			!strings.Contains(file.Path, "/competitions/Demo-Final--session-6/") ||
			!strings.Contains(file.Path, "/Team-One--entry-9/") ||
			!strings.HasSuffix(file.Path, "/Résumé-deck--attachment-17-version-2.PDF") ||
			strings.Contains(file.Path, "..") ||
			file.OriginalFilename != "../../Résumé deck..PDF" {
			t.Fatalf("unsafe or incomplete Final Files path = %+v", file)
		}
	}
	if files[0].Path == files[1].Path {
		t.Fatalf("Track collision was not disambiguated: %+v", files)
	}

	version.Tracks = nil
	untracked := finalFilesFiles(version)
	if len(untracked) != 1 || !strings.HasPrefix(untracked[0].Path, "untracked/competitions/") {
		t.Fatalf("untracked Final Files path = %+v", untracked)
	}

	version.OwnerType = store.UploadTargetPresentation
	version.OwnerID = version.SessionID
	version.EntryID, version.EntryName = 0, ""
	presentation := finalFilesFiles(version)
	if len(presentation) != 1 ||
		!strings.HasPrefix(presentation[0].Path, "untracked/presentations/Demo-Final--session-6/") {
		t.Fatalf("Presentation Final Files path = %+v", presentation)
	}
}

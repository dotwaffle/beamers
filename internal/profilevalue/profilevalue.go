// Package profilevalue contains persistence values shared by Profile schema and storage.
package profilevalue

// Entry is one released Entry selected for a Public Profile.
type Entry struct {
	ID   int    `json:"entry_id"`
	Name string `json:"name"`
}

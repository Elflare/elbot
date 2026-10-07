package contextinfo

// Model fixes the main dialogue target. Independent text calls publish their
// actual target in their own events instead of borrowing this protocol fact.
type Model struct {
	Provider string
	Model    string
	APIType  string
}

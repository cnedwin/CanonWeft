package retrieval

// NovelEdition is server-authored archive metadata, never imported by a parser
// or proposed by a model. The full manuscript exists only in the managed object.
type NovelEdition struct {
	BookID      string `json:"book_id"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Author      string `json:"author"`
	PreviewHash string `json:"preview_hash"`
	RequestHash string `json:"request_hash"`
	Included    int    `json:"included"`
	Missing     int    `json:"missing"`
}

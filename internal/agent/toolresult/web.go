package toolresult

// WebSearch is the result of a web search.
type WebSearch struct {
	Query   string          `json:"query"`
	Results []WebSearchItem `json:"results"`
}

// WebSearchItem is one search hit.
type WebSearchItem struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

func (r WebSearch) Map() map[string]any {
	if r.Results == nil {
		r.Results = []WebSearchItem{}
	}
	return toMap(r)
}

// WebFetch is the result of a page fetch. Which of the optional fields are
// present depends on the fetched format: json carries Data, html the article
// metadata, markdown and text a Length.
type WebFetch struct {
	Success      bool    `json:"success"`
	URL          string  `json:"url"`
	Provider     string  `json:"provider"`
	ProviderName string  `json:"providerName,omitempty"`
	Format       string  `json:"format"`
	ContentType  string  `json:"contentType"`
	Content      *string `json:"content,omitempty"`
	Data         *any    `json:"data,omitempty"`
	Title        *string `json:"title,omitempty"`
	Byline       *string `json:"byline,omitempty"`
	Excerpt      *string `json:"excerpt,omitempty"`
	TextContent  *string `json:"textContent,omitempty"`
	Length       *int    `json:"length,omitempty"`
}

func (r WebFetch) Map() map[string]any { return toMap(r) }

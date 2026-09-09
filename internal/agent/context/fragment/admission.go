package contextfrag

// DefaultAbsoluteCapTokens is the server-wide context admission cap applied
// when no explicit `[agent] context_absolute_max_tokens` is configured. It
// bounds context materialization even when a model has no configured context
// window (CM-ADM-001); models with larger windows need the cap raised
// explicitly. The cap is denominated in this ledger's token estimate, so a
// real tokenizer swap revisits it together with EstimateBytesPerToken.
const DefaultAbsoluteCapTokens = 200_000

package contextfrag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func CanonicalFragmentHash(frag ContextFrag) (FragmentHash, error) {
	parts, err := canonicalParts(frag.Parts)
	if err != nil {
		return FragmentHash{}, err
	}
	canonical := canonicalFragment{
		Kind:               frag.Kind,
		Role:               string(frag.Role),
		Slot:               frag.Slot,
		Priority:           frag.Priority,
		RetentionTier:      frag.RetentionTier,
		DropPriority:       frag.DropPriority,
		RequiredCapability: frag.RequiredCapability,
		CacheClass:         frag.CacheClass,
		Trust:              frag.Trust,
		Scope:              frag.Scope,
		Budget:             frag.Budget,
		Render:             frag.Render,
		Provenance: canonicalProvenance{
			Source:    frag.Provenance.Source,
			SourceID:  frag.Provenance.SourceID,
			Collector: frag.Provenance.Collector,
		},
		Parts: parts,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return FragmentHash{}, err
	}
	sum := sha256.Sum256(raw)
	return FragmentHash{
		Algo:  HashAlgoSHA256,
		Scope: HashScopeCanonicalFragment,
		Value: hex.EncodeToString(sum[:]),
	}, nil
}

type canonicalFragment struct {
	Kind               Kind                `json:"kind"`
	Role               string              `json:"role,omitempty"`
	Slot               Slot                `json:"slot"`
	Priority           int                 `json:"priority,omitempty"`
	RetentionTier      RetentionTier       `json:"retention_tier,omitempty"`
	DropPriority       DropPriority        `json:"drop_priority,omitempty"`
	RequiredCapability string              `json:"required_capability,omitempty"`
	CacheClass         CacheClass          `json:"cache_class,omitempty"`
	Trust              TrustLevel          `json:"trust,omitempty"`
	Scope              Scope               `json:"scope,omitempty"`
	Budget             BudgetPolicy        `json:"budget,omitempty"`
	Render             RenderPolicy        `json:"render,omitempty"`
	Provenance         canonicalProvenance `json:"provenance,omitempty"`
	Parts              []canonicalPart     `json:"parts,omitempty"`
}

type canonicalProvenance struct {
	Source    string `json:"source,omitempty"`
	SourceID  string `json:"source_id,omitempty"`
	Collector string `json:"collector,omitempty"`
}

type canonicalPart struct {
	Type      PartType        `json:"type"`
	Text      string          `json:"text,omitempty"`
	Image     ImageRef        `json:"image,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	ImagePart json.RawMessage `json:"image_part,omitempty"`
}

func canonicalParts(parts []Part) ([]canonicalPart, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	out := make([]canonicalPart, 0, len(parts))
	for _, part := range parts {
		next := canonicalPart{
			Type:  part.Type,
			Text:  part.Text,
			Image: part.Image,
		}
		if msg := partMessage(part); msg != nil {
			raw, err := json.Marshal(msg)
			if err != nil {
				return nil, err
			}
			next.Message = raw
		}
		if image := partSDKImage(part); image != nil {
			raw, err := json.Marshal(image)
			if err != nil {
				return nil, err
			}
			next.ImagePart = raw
		}
		out = append(out, next)
	}
	return out, nil
}

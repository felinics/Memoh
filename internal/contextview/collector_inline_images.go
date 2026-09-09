package contextview

import (
	"context"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

const inlineImagesCollectorName = "inline_images"

type InlineImageConfig struct {
	Images []sdk.ImagePart
}

type InlineImageCollector struct{}

func (*InlineImageCollector) Name() string {
	return inlineImagesCollectorName
}

func (*InlineImageCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := inlineImageConfig(req.Config)
	if err != nil {
		return nil, err
	}
	if len(cfg.Images) == 0 {
		return nil, nil
	}
	frag := contextfrag.ImageFrag("current_user.images", cfg.Images, req.Scope, contextfrag.SourceRunConfig)
	if len(frag.Parts) == 0 {
		return nil, nil
	}
	frag.Provenance.Collector = inlineImagesCollectorName
	return []contextfrag.ContextFrag{frag}, nil
}

func inlineImageConfig(config any) (InlineImageConfig, error) {
	return collectorConfig[InlineImageConfig](config, "inline_images config must be InlineImageConfig")
}

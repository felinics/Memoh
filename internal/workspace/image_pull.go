package workspace

import (
	"context"
	"errors"
	"log/slog"

	"github.com/felinics/memoh/internal/config"
	ctr "github.com/felinics/memoh/internal/container"
)

type ImagePrepareMode string

const (
	ImagePreparePulled    ImagePrepareMode = "pulled"
	ImagePrepareSkipped   ImagePrepareMode = "skipped"
	ImagePrepareDelegated ImagePrepareMode = "delegated"
)

type ImagePrepareResult struct {
	Mode     ImagePrepareMode
	ImageRef string
	Image    ctr.ImageInfo
	Message  string
}

func (m *Manager) PrepareImageForCreate(ctx context.Context, image string, opts *ctr.PullImageOptions) (ImagePrepareResult, error) {
	candidates := config.WorkspaceImagePullCandidates(image)
	if len(candidates) == 0 {
		return ImagePrepareResult{}, ctr.ErrInvalidArgument
	}
	primary := candidates[0]
	policy := m.cfg.EffectiveImagePullPolicy()
	if policy == config.ImagePullPolicyNever {
		return ImagePrepareResult{Mode: ImagePrepareSkipped, ImageRef: primary, Message: "image pull disabled by policy"}, nil
	}

	imageService, ok := m.service.(ctr.ImageService)
	if !ok {
		return ImagePrepareResult{Mode: ImagePrepareDelegated, ImageRef: primary, Message: "runtime backend handles image pulling"}, nil
	}

	if policy == config.ImagePullPolicyIfNotPresent {
		for _, candidate := range candidates {
			info, err := imageService.GetImage(ctx, candidate)
			if err == nil {
				return ImagePrepareResult{Mode: ImagePrepareSkipped, ImageRef: candidate, Image: info, Message: "image already present"}, nil
			}
			if errors.Is(err, ctr.ErrNotSupported) {
				return ImagePrepareResult{Mode: ImagePrepareDelegated, ImageRef: primary, Message: "runtime backend handles image pulling"}, nil
			}
			if !ctr.IsNotFound(err) {
				m.logger.Info("image lookup failed, attempting pull",
					slog.String("image", candidate),
					slog.Any("error", err))
			}
		}
	}

	var lastErr error
	for i, candidate := range candidates {
		info, err := imageService.PullImage(ctx, candidate, opts)
		if err == nil {
			message := "image pulled"
			if candidate != primary {
				message = "image pulled from fallback mirror"
			}
			return ImagePrepareResult{Mode: ImagePreparePulled, ImageRef: candidate, Image: info, Message: message}, nil
		}
		if errors.Is(err, ctr.ErrNotSupported) {
			return ImagePrepareResult{Mode: ImagePrepareDelegated, ImageRef: primary, Message: "runtime backend handles image pulling"}, nil
		}
		lastErr = err
		if i+1 < len(candidates) {
			m.logger.Warn("image pull failed, trying fallback image",
				slog.String("image", candidate),
				slog.String("fallback_image", candidates[i+1]),
				slog.Any("error", err))
		}
	}
	return ImagePrepareResult{}, lastErr
}

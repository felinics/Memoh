package workspace

import (
	"context"
	"errors"
	"log/slog"

	ctr "github.com/memohai/memoh/domains/runtime/container"
	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/config"
)

func (m *Manager) PrepareImageForCreate(ctx context.Context, image string, opts *ctr.PullImageOptions) (runtimeworkspace.ImagePrepareResult, error) {
	candidates := config.WorkspaceImagePullCandidates(image)
	if len(candidates) == 0 {
		return runtimeworkspace.ImagePrepareResult{}, ctr.ErrInvalidArgument
	}
	primary := candidates[0]
	policy := m.cfg.EffectiveImagePullPolicy()
	if policy == config.ImagePullPolicyNever {
		return runtimeworkspace.ImagePrepareResult{Mode: runtimeworkspace.ImagePrepareSkipped, ImageRef: primary, Message: "image pull disabled by policy"}, nil
	}

	imageService, ok := m.service.(ctr.ImageService)
	if !ok {
		return runtimeworkspace.ImagePrepareResult{Mode: runtimeworkspace.ImagePrepareDelegated, ImageRef: primary, Message: "runtime backend handles image pulling"}, nil
	}

	if policy == config.ImagePullPolicyIfNotPresent {
		for _, candidate := range candidates {
			info, err := imageService.GetImage(ctx, candidate)
			if err == nil {
				return runtimeworkspace.ImagePrepareResult{Mode: runtimeworkspace.ImagePrepareSkipped, ImageRef: candidate, Image: info, Message: "image already present"}, nil
			}
			if errors.Is(err, ctr.ErrNotSupported) {
				return runtimeworkspace.ImagePrepareResult{Mode: runtimeworkspace.ImagePrepareDelegated, ImageRef: primary, Message: "runtime backend handles image pulling"}, nil
			}
			if !ctr.IsNotFound(err) {
				m.logger.InfoContext(ctx, "image lookup failed, attempting pull",
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
			return runtimeworkspace.ImagePrepareResult{Mode: runtimeworkspace.ImagePreparePulled, ImageRef: candidate, Image: info, Message: message}, nil
		}
		if errors.Is(err, ctr.ErrNotSupported) {
			return runtimeworkspace.ImagePrepareResult{Mode: runtimeworkspace.ImagePrepareDelegated, ImageRef: primary, Message: "runtime backend handles image pulling"}, nil
		}
		lastErr = err
		if i+1 < len(candidates) {
			m.logger.WarnContext(ctx, "image pull failed, trying fallback image",
				slog.String("image", candidate),
				slog.String("fallback_image", candidates[i+1]),
				slog.Any("error", err))
		}
	}
	return runtimeworkspace.ImagePrepareResult{}, lastErr
}

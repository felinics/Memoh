package workspace

import ctr "github.com/memohai/memoh/domains/runtime/container"

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

package contextview

import "errors"

// collectorConfig unwraps a collector's CollectRequest.Config into its
// concrete type T, accepting a nil config, a direct value, or a pointer
// (nil or populated), and rejecting anything else with errMsg.
func collectorConfig[T any](config any, errMsg string) (T, error) {
	var zero T
	if config == nil {
		return zero, nil
	}
	switch cfg := config.(type) {
	case *T:
		if cfg == nil {
			return zero, nil
		}
		return *cfg, nil
	case T:
		return cfg, nil
	default:
		return zero, errors.New(errMsg)
	}
}

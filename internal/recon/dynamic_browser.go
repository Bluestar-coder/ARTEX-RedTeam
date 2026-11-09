package recon

import (
	"context"
	"fmt"
)

// captureBrowser is implemented in the browser-launch commit. Keeping this
// stub in the provider-skeleton commit makes the provider compile and keeps the
// high-risk browser lifecycle independently revertable.
func captureBrowser(_ context.Context, pageURL string, _ Scope, _ DynamicConfig) (DynamicCapture, error) {
	return DynamicCapture{PageURL: pageURL}, fmt.Errorf("dynamic browser capture not implemented in this build")
}

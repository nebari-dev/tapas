//go:build !local

package servercmd

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/config"
)

// memoryKeyOf reads mem0's admin key from the Secret the mem0 pack made in
// mem0's own namespace, through c; nothing, and no read, while memory is
// off. The value is never put in an error or a log.
func memoryKeyOf(ctx context.Context, c client.Client, m config.MemoryCfg) (string, error) {
	if !m.Enabled {
		return "", nil
	}
	ref := m.APIKeySecret
	var sec corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, &sec); err != nil {
		return "", fmt.Errorf("read the mem0 key Secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	key := strings.TrimSpace(string(sec.Data[ref.Key]))
	if key == "" {
		return "", fmt.Errorf("Secret %s/%s has no %q", ref.Namespace, ref.Name, ref.Key)
	}
	return key, nil
}

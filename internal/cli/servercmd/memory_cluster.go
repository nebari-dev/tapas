//go:build !local

package servercmd

import (
	"context"
	"fmt"
	"unicode"
	"unicode/utf8"

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
	key := string(sec.Data[ref.Key])
	if key == "" {
		return "", fmt.Errorf("Secret %s/%s has no %q", ref.Namespace, ref.Name, ref.Key)
	}
	// mem0 compares the key to the X-API-Key header exactly, so a padded
	// value can never work. Refuse it rather than quietly trim it.
	first, _ := utf8.DecodeRuneInString(key)
	last, _ := utf8.DecodeLastRuneInString(key)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return "", fmt.Errorf("Secret %s/%s key %q has leading or trailing whitespace: mem0 compares the key exactly, so fix the Secret", ref.Namespace, ref.Name, ref.Key)
	}
	return key, nil
}

//go:build !local

package servercmd

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nebari-dev/javo/internal/config"
)

func memSecret(ns, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: data}
}

// countingClient counts Gets so a test can say the Secret was not read.
type countingClient struct {
	client.Client
	gets int
}

func (c *countingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.gets++
	return c.Client.Get(ctx, key, obj, opts...)
}

// The key is read from the Secret in mem0's namespace, and a missing or
// empty key is an error that names the Secret and never prints a value.
func TestMemoryKeyIsReadFromMem0sNamespace(t *testing.T) {
	ref := config.SecretKeyRef{Namespace: "mem0", Name: "mem0-admin-key", Key: "api-key"}
	tests := []struct {
		name     string
		objs     []client.Object
		want     string
		wantErr  []string
		mustHide string
	}{
		{
			name: "trims the value",
			objs: []client.Object{memSecret("mem0", "mem0-admin-key", map[string][]byte{"api-key": []byte("abc123\n")})},
			want: "abc123",
		},
		{
			name:    "no Secret",
			wantErr: []string{"mem0/mem0-admin-key"},
		},
		{
			name:    "no such key",
			objs:    []client.Object{memSecret("mem0", "mem0-admin-key", map[string][]byte{"other": []byte("sekret-other")})},
			wantErr: []string{"mem0/mem0-admin-key", "api-key"}, mustHide: "sekret-other",
		},
		{
			name:    "empty value",
			objs:    []client.Object{memSecret("mem0", "mem0-admin-key", map[string][]byte{"api-key": []byte(" \n")})},
			wantErr: []string{"mem0/mem0-admin-key", "api-key"},
		},
		{
			name:    "same name in another namespace is not used",
			objs:    []client.Object{memSecret("javo-system", "mem0-admin-key", map[string][]byte{"api-key": []byte("wrong-ns-key")})},
			wantErr: []string{"mem0/mem0-admin-key"}, mustHide: "wrong-ns-key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithObjects(tt.objs...).Build()
			got, err := memoryKeyOf(context.Background(), c, config.MemoryCfg{Enabled: true, APIKeySecret: ref})
			if len(tt.wantErr) == 0 {
				if err != nil || got != tt.want {
					t.Fatalf("got %q, %v; want %q", got, err, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("got %q and no error", got)
			}
			for _, s := range tt.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q lacks %q", err, s)
				}
			}
			if tt.mustHide != "" && strings.Contains(err.Error(), tt.mustHide) {
				t.Errorf("error %q holds a key value", err)
			}
			if got != "" {
				t.Errorf("key %q returned with an error", got)
			}
		})
	}
}

// With memory off the Secret is not read, whether or not it exists.
func TestNoMemoryMeansNoSecretRead(t *testing.T) {
	c := &countingClient{Client: fake.NewClientBuilder().Build()}
	got, err := memoryKeyOf(context.Background(), c, config.MemoryCfg{
		APIKeySecret: config.SecretKeyRef{Namespace: "mem0", Name: "mem0-admin-key", Key: "api-key"},
	})
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
	if c.gets != 0 {
		t.Fatalf("%d Gets with memory off", c.gets)
	}
}

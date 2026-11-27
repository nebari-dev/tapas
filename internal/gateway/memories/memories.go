// Package memories serves a person's long-term memories: they list what Tapas
// remembers about them and delete any of it. It never adds or changes one.
package memories

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/memory"
	"github.com/nebari-dev/javo/internal/reasons"
)

const memoriesRoute = "/api/memories"

// Store is the memory service the routes read and change; *memory.Service
// satisfies it. Delete checks that the memory is the owner's.
type Store interface {
	List(ctx context.Context, owner string) ([]memory.Memory, error)
	Delete(ctx context.Context, owner, id string) error
}

// Forgetter drops what the recall cache holds for a person, so a deleted
// memory is not recalled from the cache; *agentproxy.RecallCache satisfies it.
type Forgetter interface {
	Forget(owner string)
}

// API is the memories routes. A nil Memories means memory is off on this
// platform: the routes answer 404 and say so.
type API struct {
	Memories Store
	Cache    Forgetter
}

// MemoryView is one memory as the page shows it.
type MemoryView struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type idInput struct {
	ID string `path:"id" doc:"The memory's id"`
}

// Register adds the memories routes.
func (a *API) Register(api huma.API) {
	tagged := func(op huma.Operation) huma.Operation {
		op.Tags = []string{"memories"}
		op.Errors = append(op.Errors, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway)
		return op
	}

	huma.Register(api, tagged(huma.Operation{
		OperationID: "listMemories", Method: http.MethodGet, Path: memoriesRoute,
		Summary:     "What Tapas remembers about you, newest first",
		Description: "404 when memory is off on this platform; 502 when the memory service cannot be reached.",
	}), func(ctx context.Context, _ *struct{}) (*struct{ Body []MemoryView }, error) {
		if err := authn.NoAppTokens(ctx); err != nil {
			return nil, err
		}
		if a.Memories == nil {
			return nil, off()
		}
		list, err := a.Memories.List(ctx, authn.User(ctx).ID)
		if err != nil {
			return nil, unreachable("memory.list_failed", authn.User(ctx).ID, err)
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
		out := &struct{ Body []MemoryView }{Body: make([]MemoryView, 0, len(list))}
		for _, m := range list {
			out.Body = append(out.Body, MemoryView{ID: m.ID, Text: m.Text, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt})
		}
		return out, nil
	})

	huma.Register(api, tagged(huma.Operation{
		OperationID: "deleteMemory", Method: http.MethodDelete, Path: memoriesRoute + "/{id}",
		Summary:       "Delete one of your memories",
		Description:   "Another person's memory is a 404, the same as one that does not exist.",
		DefaultStatus: http.StatusNoContent,
	}), func(ctx context.Context, in *idInput) (*struct{}, error) {
		if err := authn.NoAppTokens(ctx); err != nil {
			return nil, err
		}
		if a.Memories == nil {
			return nil, off()
		}
		owner := authn.User(ctx).ID
		err := a.Memories.Delete(ctx, owner, in.ID)
		switch {
		case errors.Is(err, memory.ErrNotFound):
			return nil, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgMemoryNotFound))
		case err != nil:
			return nil, unreachable("memory.delete_failed", owner, err)
		}
		if a.Cache != nil {
			a.Cache.Forget(owner)
		}
		return nil, nil
	})
}

func off() error {
	return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgMemoryOff))
}

// unreachable logs event with the person's id and the memory service's error
// here and answers with the catalog's 502 title alone, so the error text never
// reaches the body.
func unreachable(event, user string, err error) error {
	slog.Warn(event, "user", user, "err", err)
	return problem.New(http.StatusBadGateway, reasons.Msg(reasons.MsgMemoryUnreachable))
}

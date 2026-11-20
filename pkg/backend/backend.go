package backend

import (
	"context"
	"io"

	"github.com/frinknet/caddy-githost/pkg/access"
	"github.com/frinknet/caddy-githost/pkg/repo"
)

type Request struct {
	Repo        repo.Repo
	Root        string
	Dir         string
	Handle      string
	Role        access.Role
	GitProtocol string
	Admin       bool
	Access      *access.Store
}

type Backend interface {
	UploadPack(ctx context.Context, req Request, stdin io.Reader, stdout, stderr io.Writer) error
	ReceivePack(ctx context.Context, req Request, stdin io.Reader, stdout, stderr io.Writer) error
}

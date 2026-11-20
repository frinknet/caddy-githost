package backend

import (
	"context"
	"io"

	"github.com/frinknet/caddy-gitproxy/pkg/access"
	"github.com/frinknet/caddy-gitproxy/pkg/repo"
)

type Request struct {
	Repo        repo.Repo
	Root        string
	Dir         string
	Handle      string
	Role        access.Role
	GitProtocol string
}

type Backend interface {
	UploadPack(ctx context.Context, req Request, stdin io.Reader, stdout, stderr io.Writer) error
	ReceivePack(ctx context.Context, req Request, stdin io.Reader, stdout, stderr io.Writer) error
}

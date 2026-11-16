package main

// Proof of concept, Step 1: links.
//
//	item link SRC LINK_TYPE TARGET [--etag E] [--request-id ID]
//	item unlink SRC LINK_TYPE TARGET [--etag E] [--request-id ID]
//
// LINK_TYPE is the declared name (blocks, never blocked_by). The event and
// the etag are the source's. item get prints an Item's links.

import (
	"context"
	"flag"
	"strings"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// linkArgs reads SRC LINK_TYPE TARGET and the flags both commands take.
func (c *cli) linkArgs(name string, args []string) (src, linkType, dst, etag, requestID string, err error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	e := fs.String("etag", "", "write only if the source is still at this etag")
	id := fs.String("request-id", newRequestID(), "idempotency key")
	pos, err := parse(fs, args)
	if err != nil {
		return "", "", "", "", "", err
	}
	if len(pos) != 3 {
		return "", "", "", "", "", usagef("%s wants SRC LINK_TYPE TARGET, got %d arguments", name, len(pos))
	}
	return c.itemRef(pos[0]), pos[1], c.itemRef(pos[2]), *e, *id, nil
}

func (c *cli) itemLink(ctx context.Context, args []string) error {
	src, lt, dst, etag, id, err := c.linkArgs("item link", args)
	if err != nil {
		return err
	}
	res, err := c.c.LinkItems(ctx, connect.NewRequest(&kernelv1.LinkItemsRequest{
		Name: src, RequestId: id, Etag: etag, LinkType: lt, Target: dst,
	}))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}

func (c *cli) itemUnlink(ctx context.Context, args []string) error {
	src, lt, dst, etag, id, err := c.linkArgs("item unlink", args)
	if err != nil {
		return err
	}
	res, err := c.c.UnlinkItems(ctx, connect.NewRequest(&kernelv1.UnlinkItemsRequest{
		Name: src, RequestId: id, Etag: etag, LinkType: lt, Target: dst,
	}))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}

// linked reads --linked out:NAME=ITEM or in:NAME=ITEM: the name as the
// results see it (out: a declared name, in: an inverse name). The prefix is
// required, so the direction is never guessed.
func linked(ns, raw string) (*kernelv1.LinkFilter, error) {
	bad := usagef("--linked wants out:NAME=ITEM or in:NAME=ITEM, not %q", raw)
	dir, rest, ok := strings.Cut(raw, ":")
	if !ok {
		return nil, bad
	}
	lf := &kernelv1.LinkFilter{}
	switch dir {
	case "out":
		lf.Direction = kernelv1.LinkDirection_LINK_DIRECTION_OUTGOING
	case "in":
		lf.Direction = kernelv1.LinkDirection_LINK_DIRECTION_INCOMING
	default:
		return nil, bad
	}
	name, ref, ok := strings.Cut(rest, "=")
	if !ok || name == "" || ref == "" {
		return nil, bad
	}
	lf.LinkType, lf.Item = name, ref
	if !strings.Contains(ref, "/") {
		lf.Item = "namespaces/" + ns + "/items/" + ref
	}
	return lf, nil
}

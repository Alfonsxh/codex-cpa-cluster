package notifications

import (
	"context"
	"errors"
)

type WriteFence interface {
	WithWriteFence(context.Context, func() error) error
}

// FencedSender keeps the non-idempotent Webhook request inside the exact
// runtime/worker lease generation. Runtime-state persistence intentionally
// remains outside this wrapper to avoid recursively taking the control-plane
// lock; a crash after remote acceptance therefore retains at-least-once
// delivery semantics.
type FencedSender struct {
	sender ContentSender
	fence  WriteFence
}

func NewFencedSender(sender ContentSender, fence WriteFence) (*FencedSender, error) {
	if sender == nil || fence == nil {
		return nil, errors.New("fenced notification sender requires a sender and ownership fence")
	}
	return &FencedSender{sender: sender, fence: fence}, nil
}

func (sender *FencedSender) Configured(ctx context.Context) (bool, error) {
	return sender.sender.Configured(ctx)
}

func (sender *FencedSender) Send(ctx context.Context, content string) (SendResult, error) {
	var result SendResult
	err := sender.fence.WithWriteFence(ctx, func() error {
		var sendError error
		result, sendError = sender.sender.Send(ctx, content)
		return sendError
	})
	return result, err
}

var _ ContentSender = (*FencedSender)(nil)

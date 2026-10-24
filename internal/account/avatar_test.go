package account

import (
	"context"
	"errors"
	"testing"
)

func TestAvatarCapacityRejectsBeforeDecoding(t *testing.T) {
	service := &Service{avatarSlots: make(chan struct{}, 1)}
	service.avatarSlots <- struct{}{}
	if _, err := service.SetAvatar(context.Background(), "id", []byte("not an image")); !errors.Is(err, ErrAvatarBusy) {
		t.Fatalf("busy decoder should reject work before decoding: %v", err)
	}
	<-service.avatarSlots
	if _, err := service.SetAvatar(context.Background(), "id", []byte("not an image")); !errors.Is(err, ErrAvatar) {
		t.Fatalf("decoder capacity not released: %v", err)
	}
}

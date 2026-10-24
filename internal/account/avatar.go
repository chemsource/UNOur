package account

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
)

const MaxAvatarBytes = 2 << 20

func (s *Service) SetAvatar(ctx context.Context, id string, data []byte) (Profile, error) {
	if len(data) > MaxAvatarBytes {
		return Profile{}, ErrAvatarTooLarge
	}
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	// A tiny compressed image can expand to over 100 MiB. Bound the complete
	// decode/resize lifetime across all authenticated accounts in this service.
	select {
	case s.avatarSlots <- struct{}{}:
		defer func() { <-s.avatarSlots }()
	default:
		return Profile{}, ErrAvatarBusy
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 16000000 {
		return Profile{}, ErrAvatar
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Profile{}, ErrAvatar
	}
	bounds := source.Bounds()
	edge := min(bounds.Dx(), bounds.Dy())
	left := bounds.Min.X + (bounds.Dx()-edge)/2
	top := bounds.Min.Y + (bounds.Dy()-edge)/2
	normalized := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	draw.CatmullRom.Scale(normalized, normalized.Bounds(), source, image.Rect(left, top, left+edge, top+edge), draw.Src, nil)
	var out bytes.Buffer
	if err := png.Encode(&out, normalized); err != nil {
		return Profile{}, err
	}
	digest := sha256.Sum256(out.Bytes())
	return s.store.SetAccountAvatar(ctx, id, hex.EncodeToString(digest[:]), out.Bytes())
}
func (s *Service) ResetAvatar(ctx context.Context, id string) (Profile, error) {
	return s.store.SetAccountAvatar(ctx, id, "", nil)
}
func (s *Service) Avatar(ctx context.Context, digest string) ([]byte, error) {
	if len(digest) != 64 {
		return nil, ErrAvatarNotFound
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return nil, ErrAvatarNotFound
	}
	return s.store.AccountAvatar(ctx, digest)
}

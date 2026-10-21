package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/carrierstore"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/provider"
)

func providerCarriers(cs []event.Carrier) []provider.Carrier {
	if len(cs) == 0 {
		return nil
	}
	out := make([]provider.Carrier, len(cs))
	for i, c := range cs {
		out[i] = provider.Carrier{Family: c.Family, Model: c.Model, Kind: provider.CarrierKind(c.Kind), Anchor: c.Anchor, Raw: c.Raw, Replayable: c.Replayable, SHA: c.SHA}
	}
	return out
}

func eventCarriers(cs []provider.Carrier) []event.Carrier {
	if len(cs) == 0 {
		return nil
	}
	out := make([]event.Carrier, len(cs))
	for i, c := range cs {
		out[i] = event.Carrier{Family: c.Family, Model: c.Model, Kind: string(c.Kind), Anchor: c.Anchor, Raw: c.Raw, Replayable: c.Replayable, SHA: c.SHA}
	}
	return out
}

func eventDrops(ds []provider.CarrierDrop) []event.CarrierDrop {
	if len(ds) == 0 {
		return nil
	}
	out := make([]event.CarrierDrop, len(ds))
	for i, d := range ds {
		out[i] = event.CarrierDrop{Reason: d.Reason, Count: d.Count}
	}
	return out
}

type ctxCarrierStore struct{ db *ctxdb.Store }

func (s *ctxCarrierStore) SaveCarriers(ctx context.Context, sid string, eventID int64, cs []event.Carrier) error {
	rows := make([]carrierstore.Carrier, len(cs))
	for i, c := range cs {
		rows[i] = carrierstore.Carrier{Ord: i, Family: c.Family, Model: c.Model, Kind: carrierstore.Kind(c.Kind), Anchor: c.Anchor, Payload: c.Raw, Replayable: c.Replayable, CreatedAt: time.Now().UnixMilli()}
	}
	if _, err := (carrierstore.Store{DB: s.db}).Save(ctx, sid, eventID, rows); err != nil {
		return fmt.Errorf("save carriers: %w", err)
	}
	return nil
}

func (s *ctxCarrierStore) RejectCarriers(ctx context.Context, sid string, cs []event.Carrier, code string) error {
	shas := make([]string, len(cs))
	for i := range cs {
		sha := cs[i].SHA
		if sha == "" {
			sum := sha256.Sum256(cs[i].Raw)
			sha = hex.EncodeToString(sum[:])
		}
		shas[i] = sha
	}
	store := carrierstore.Store{DB: s.db}
	ids, err := store.BySHA(ctx, sid, shas)
	if err != nil {
		return fmt.Errorf("find rejected carriers: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if err := store.Reject(ctx, sid, ids, code, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("record rejection: %w", err)
	}
	return nil
}

func (s *ctxStore) loadCarriers(ctx context.Context, sid string, at map[int64]int, out []loop.Message) error {
	ids := make([]int64, 0, len(at))
	for id := range at {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	store := carrierstore.Store{DB: s.db}
	stored, err := store.Load(ctx, sid, ids)
	if err != nil {
		return fmt.Errorf("load carriers: %w", err)
	}
	rejected, err := store.Rejected(ctx, sid)
	if err != nil {
		return fmt.Errorf("load carrier rejections: %w", err)
	}
	byEvent := make(map[int64][]event.Carrier, len(stored))
	for i := range stored {
		st := &stored[i]
		if rejected[st.ID] {
			continue // never replay a signature the provider rejected
		}
		byEvent[st.EventID] = append(byEvent[st.EventID], event.Carrier{Family: st.Family, Model: st.Model, Kind: string(st.Kind), Anchor: st.Anchor, Raw: st.Payload, Replayable: st.Replayable, SHA: st.SHA})
	}
	for id, i := range at {
		out[i].Carriers = byEvent[id]
	}
	return nil
}

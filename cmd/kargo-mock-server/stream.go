package main

import (
	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	svcv1alpha1 "unknwon.dev/kargo-tui/internal/kargoapi/svc"
)

// streamSubscriber is one open WatchStages stream. Events fan out through
// the channel; on close the broadcaster removes the subscriber.
type streamSubscriber struct {
	project string
	events  chan *svcv1alpha1.WatchStagesResponse
	done    chan struct{}
}

func newSubscriber(project string) *streamSubscriber {
	return &streamSubscriber{
		project: project,
		events:  make(chan *svcv1alpha1.WatchStagesResponse, 64),
		done:    make(chan struct{}),
	}
}

func (sub *streamSubscriber) close() {
	select {
	case <-sub.done:
	default:
		close(sub.done)
	}
}

// subscribe adds a subscriber to this project and replays the current
// stage set as an initial snapshot (one ADDED event per stage). The
// snapshot is pushed asynchronously so we don't block on the bounded
// channel before the consumer has started draining.
func (p *projectState) subscribe() *streamSubscriber {
	sub := newSubscriber(p.name)
	p.subsMu.Lock()
	if p.subscribers == nil {
		p.subscribers = make(map[*streamSubscriber]struct{})
	}
	p.subscribers[sub] = struct{}{}
	p.subsMu.Unlock()

	stages := p.listStages()
	go func() {
		for _, s := range stages {
			select {
			case sub.events <- &svcv1alpha1.WatchStagesResponse{Type: "ADDED", Stage: s}:
			case <-sub.done:
				return
			}
		}
	}()
	return sub
}

func (p *projectState) unsubscribe(sub *streamSubscriber) {
	p.subsMu.Lock()
	delete(p.subscribers, sub)
	p.subsMu.Unlock()
	sub.close()
}

// broadcastStage fan-outs a MODIFIED event for the given stage to every
// open subscriber on this project. Non-blocking: a slow subscriber that
// fills its 64-buffer just drops events (the TUI re-syncs via ListStages
// on the next refresh tick).
func (p *projectState) broadcastStage(s *kargoapi.Stage) {
	if s == nil {
		return
	}
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	resp := &svcv1alpha1.WatchStagesResponse{Type: "MODIFIED", Stage: s}
	for sub := range p.subscribers {
		select {
		case sub.events <- resp:
		default:
		}
	}
}

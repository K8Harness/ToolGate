package main

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

type Session struct {
	ID        string
	CreatedAt time.Time
}

type SessionRegistry struct {
	m sync.Map
}

func (r *SessionRegistry) Create() *Session {
	session := Session{
		ID:        newSessionID(),
		CreatedAt: time.Now(),
	}
	r.m.Store(session.ID, session)
	return &session
}

func (r *SessionRegistry) Get(id string) (*Session, bool) {
	value, ok := r.m.Load(id)
	if !ok {
		return nil, false
	}

	session, ok := value.(Session)
	if !ok {
		return nil, false
	}
	return &session, true
}

func (r *SessionRegistry) Delete(id string) {
	r.m.Delete(id)
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("generate session ID: %v", err))
	}

	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

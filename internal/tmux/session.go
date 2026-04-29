package tmux

import (
	"fmt"
	"sync"
)

type Pane struct {
	ID     string
	Output string
	Active bool
}

type Window struct {
	ID     string
	Name   string
	Panes  []*Pane
	Active bool
}

func (w *Window) FindPane(id string) *Pane {
	for _, p := range w.Panes {
		if p.ID == id {
			return p
		}
	}
	return nil
}

type Session struct {
	ID             string
	Name           string
	Windows        []*Window
	ActiveWindowID string
}

func (s *Session) FindWindow(id string) *Window {
	for _, w := range s.Windows {
		if w.ID == id {
			return w
		}
	}
	return nil
}

func (s *Session) ActiveWindow() *Window {
	return s.FindWindow(s.ActiveWindowID)
}

func (s *Session) ActivePane() *Pane {
	w := s.ActiveWindow()
	if w == nil {
		return nil
	}
	for _, p := range w.Panes {
		if p.Active {
			return p
		}
	}
	if len(w.Panes) > 0 {
		return w.Panes[0]
	}
	return nil
}

type StateMirror struct {
	mu            sync.RWMutex
	sessions      map[string]*Session // keyed by session id ($0, $1, ...)
	byName        map[string]*Session // keyed by session name
	lastSessionID string              // most recently changed session
}

func NewStateMirror() *StateMirror {
	return &StateMirror{
		sessions: make(map[string]*Session),
		byName:   make(map[string]*Session),
	}
}

func (sm *StateMirror) Apply(n Notification) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch n.Type {
	case NotifSessionChanged:
		sm.upsertSession(n.SessionID, n.SessionName)
		sm.lastSessionID = n.SessionID

	case NotifSessionRenamed:
		s := sm.sessions[n.SessionID]
		if s == nil {
			return fmt.Errorf("session %s not found for rename", n.SessionID)
		}
		delete(sm.byName, s.Name)
		s.Name = n.SessionName
		sm.byName[s.Name] = s

	case NotifSessionWindowChanged:
		sm.lastSessionID = n.SessionID
		s := sm.sessions[n.SessionID]
		if s != nil {
			s.ActiveWindowID = n.WindowID
		}

	case NotifWindowAdd:
		// Attach to the most recently changed session.
		if s := sm.sessions[sm.lastSessionID]; s != nil {
			if s.FindWindow(n.WindowID) == nil {
				s.Windows = append(s.Windows, &Window{ID: n.WindowID})
			}
		}

	case NotifWindowClose:
		for _, s := range sm.sessions {
			for i, w := range s.Windows {
				if w.ID == n.WindowID {
					s.Windows = append(s.Windows[:i], s.Windows[i+1:]...)
					return nil
				}
			}
		}

	case NotifWindowRenamed:
		for _, s := range sm.sessions {
			if w := s.FindWindow(n.WindowID); w != nil {
				w.Name = n.SessionName
				break
			}
		}

	case NotifWindowPaneChanged:
		for _, s := range sm.sessions {
			if w := s.FindWindow(n.WindowID); w != nil {
				// Mark only this pane as active.
				for _, p := range w.Panes {
					p.Active = p.ID == n.PaneID
				}
				if w.FindPane(n.PaneID) == nil {
					w.Panes = append(w.Panes, &Pane{ID: n.PaneID, Active: true})
				}
				break
			}
		}

	case NotifOutput, NotifExtendedOutput:
		for _, s := range sm.sessions {
			if p := sm.findPaneByID(s, n.PaneID); p != nil {
				p.Output += n.Data
				return nil
			}
		}

	case NotifPaneModeChanged:
		// Mode change notification; no state update needed for now.

	case NotifSessionsChanged:
		// Bulk change signal; no individual updates.

	case NotifLayoutChange:
		// Layout info available via n.Layout if needed.

	case NotifClientSessionChanged, NotifClientDetached:
		// Client lifecycle events; no session state change.

	case NotifPause, NotifContinue:
		// Flow control; no state change.

	case NotifBegin, NotifEnd, NotifError:
		// Command response guards; no state change.

	case NotifSubscriptionChanged:
		// Subscription event; no state change.

	case NotifUnknown:
		// Skip unknown notifications.
	}

	return nil
}

func (sm *StateMirror) Sessions() []*Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]*Session, 0, len(sm.sessions))
	for _, s := range sm.sessions {
		result = append(result, s)
	}
	return result
}

func (sm *StateMirror) FindSession(name string) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.byName[name]
}

func (sm *StateMirror) FindSessionByID(id string) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessions[id]
}

func (sm *StateMirror) RemoveSession(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s := sm.sessions[id]
	if s != nil {
		delete(sm.byName, s.Name)
		delete(sm.sessions, id)
	}
}

func (sm *StateMirror) upsertSession(id, name string) {
	if s, ok := sm.sessions[id]; ok {
		if name != "" && s.Name != name {
			delete(sm.byName, s.Name)
			s.Name = name
			sm.byName[name] = s
		}
		return
	}
	s := &Session{ID: id, Name: name}
	sm.sessions[id] = s
	if name != "" {
		sm.byName[name] = s
	}
}

func (sm *StateMirror) findPaneByID(s *Session, paneID string) *Pane {
	for _, w := range s.Windows {
		if p := w.FindPane(paneID); p != nil {
			return p
		}
	}
	return nil
}

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Task struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	Content         string  `json:"content"`
	Column          string  `json:"column"`
	Order           int     `json:"order"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	AssignedSession *string `json:"assigned_session"`
}

type taskFile struct {
	Tasks []Task `json:"tasks"`
}

type TaskStore struct {
	mu   sync.Mutex
	path string
}

func NewTaskStore() (*TaskStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".makro")
	os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, "tasks.json")
	return &TaskStore{path: path}, nil
}

func (s *TaskStore) Load() ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var f taskFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Tasks, nil
}

func (s *TaskStore) save(tasks []Task) error {
	f := taskFile{Tasks: tasks}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func (s *TaskStore) Create(title, content, column string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, _ := s.loadUnlocked()
	now := time.Now()
	t := Task{
		ID:        fmt.Sprintf("%d", now.UnixNano()),
		Title:     title,
		Content:   content,
		Column:    column,
		Order:     len(tasks),
		CreatedAt: now.Format(time.RFC3339),
		UpdatedAt: now.Format(time.RFC3339),
	}
	tasks = append(tasks, t)
	if err := s.save(tasks); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *TaskStore) Update(id string, patch map[string]any) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, _ := s.loadUnlocked()
	for i, t := range tasks {
		if t.ID != id {
			continue
		}
		if v, ok := patch["title"]; ok {
			tasks[i].Title = v.(string)
		}
		if v, ok := patch["content"]; ok {
			tasks[i].Content = v.(string)
		}
		if v, ok := patch["column"]; ok {
			tasks[i].Column = v.(string)
		}
		if v, ok := patch["order"]; ok {
			if n, ok := v.(float64); ok {
				tasks[i].Order = int(n)
			}
		}
		if v, ok := patch["assigned_session"]; ok {
			if v == nil {
				tasks[i].AssignedSession = nil
			} else if s, ok := v.(string); ok {
				tasks[i].AssignedSession = &s
			}
		}
		tasks[i].UpdatedAt = time.Now().Format(time.RFC3339)
		if err := s.save(tasks); err != nil {
			return nil, err
		}
		cp := tasks[i]
		return &cp, nil
	}
	return nil, fmt.Errorf("task %s not found", id)
}

func (s *TaskStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, _ := s.loadUnlocked()
	for i, t := range tasks {
		if t.ID == id {
			tasks = append(tasks[:i], tasks[i+1:]...)
			return s.save(tasks)
		}
	}
	return nil
}

func (s *TaskStore) Get(id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, _ := s.loadUnlocked()
	for _, t := range tasks {
		if t.ID == id {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("task %s not found", id)
}

func (s *TaskStore) loadUnlocked() ([]Task, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, nil
	}
	var f taskFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Tasks, nil
}

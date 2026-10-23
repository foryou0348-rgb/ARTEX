package server

import "context"

// Route-only fixtures must not execute persisted tasks from the shared test DB.
// Cancel before construction, then join the restored routines before returning.
func newRouteTestServer(m *Manager, skillDir, dataDir, keyDir string) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := New(ctx, m, skillDir, dataDir, keyDir)
	stopTestServer(s, cancel)
	return s
}

// Join task routines and file writers before the manager or TempDirs are closed.
// Live lifecycle tests call this with their own cancel function at teardown.
func stopTestServer(s *Server, cancel context.CancelFunc) {
	cancel()
	for _, task := range s.m.List() {
		s.engine.StopTask(task.ID)
	}
	s.archiveWG.Wait()
	<-s.side.done
}

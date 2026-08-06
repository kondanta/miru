// Package queue implements the per-user download queue. Each user gets one
// goroutine that processes jobs sequentially: queued → downloading → done | failed.
package queue

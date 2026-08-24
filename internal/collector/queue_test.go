package collector

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQueueUsesOnlyRESP2AuthAndLPop(t *testing.T) {
	server := newQueueServer(t, []queueReply{
		{command: []string{"AUTH", "management-secret"}, response: "+OK\r\n"},
		{command: []string{"LPOP", "usage", "2"}, response: "*2\r\n$3\r\none\r\n$3\r\ntwo\r\n"},
		{command: []string{"LPOP", "usage", "2"}, response: "*1\r\n$5\r\nthree\r\n"},
	})
	defer server.Close()

	queue, err := NewQueue(QueueConfig{
		Address: server.Address(), Password: "management-secret", BatchSize: 2,
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer queue.Close()

	var batches []string
	if err := queue.Drain(context.Background(), func(items [][]byte) error {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			parts = append(parts, string(item))
		}
		batches = append(batches, strings.Join(parts, ","))
		return nil
	}); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := strings.Join(batches, "|"); got != "one,two|three" {
		t.Fatalf("batches = %q", got)
	}
	server.Wait()
}

func TestQueueStopsOnConsumerError(t *testing.T) {
	server := newQueueServer(t, []queueReply{
		{command: []string{"AUTH", "secret"}, response: "+OK\r\n"},
		{command: []string{"LPOP", "events", "1"}, response: "*1\r\n$3\r\none\r\n"},
	})
	defer server.Close()
	queue, err := NewQueue(QueueConfig{
		Address: server.Address(), Password: "secret", Name: "events", BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	defer queue.Close()
	want := errors.New("stop")
	err = queue.Drain(context.Background(), func([][]byte) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Drain error = %v", err)
	}
	server.Wait()
}

func TestQueueValidatesBatchSize(t *testing.T) {
	for _, batchSize := range []int{0, 501} {
		if _, err := NewQueue(QueueConfig{Address: "127.0.0.1:8317", Password: "secret", BatchSize: batchSize}); err == nil {
			t.Fatalf("batch size %d was accepted", batchSize)
		}
	}
}

func TestQueueRequiresPassword(t *testing.T) {
	if _, err := NewQueue(QueueConfig{Address: "127.0.0.1:8317", BatchSize: 1}); err == nil {
		t.Fatal("empty usage queue password was accepted")
	}
}

func TestQueueRedactsPasswordFromServerErrors(t *testing.T) {
	server := newQueueServer(t, []queueReply{
		{command: []string{"AUTH", "sensitive-secret"}, response: "-ERR rejected sensitive-secret\r\n"},
	})
	defer server.Close()
	queue, err := NewQueue(QueueConfig{
		Address: server.Address(), Password: "sensitive-secret", BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	err = queue.Drain(context.Background(), func([][]byte) error { return nil })
	if err == nil || strings.Contains(err.Error(), "sensitive-secret") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("redacted error = %v", err)
	}
	server.Wait()
}

type queueReply struct {
	command  []string
	response string
}

type queueServer struct {
	t        *testing.T
	listener net.Listener
	done     chan struct{}
	once     sync.Once
}

func newQueueServer(t *testing.T, replies []queueReply) *queueServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &queueServer{t: t, listener: listener, done: make(chan struct{})}
	go func() {
		defer close(server.done)
		connection, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				t.Errorf("accept: %v", err)
			}
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		for _, reply := range replies {
			command, err := readRESPCommand(reader)
			if err != nil {
				t.Errorf("read command: %v", err)
				return
			}
			if !equalCommand(command, reply.command) {
				t.Errorf("command = %#v, want %#v", command, reply.command)
				return
			}
			if _, err := fmt.Fprint(connection, reply.response); err != nil {
				t.Errorf("write response: %v", err)
				return
			}
		}
	}()
	return server
}

func (server *queueServer) Address() string { return server.listener.Addr().String() }

func (server *queueServer) Wait() {
	server.t.Helper()
	select {
	case <-server.done:
	case <-time.After(3 * time.Second):
		server.t.Fatal("queue server did not finish")
	}
}

func (server *queueServer) Close() {
	server.once.Do(func() { _ = server.listener.Close() })
}

func readRESPCommand(reader *bufio.Reader) ([]string, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(header, "*") {
		return nil, fmt.Errorf("unexpected RESP header %q", header)
	}
	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "*")))
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, count)
	for range count {
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lengthLine, "$")))
		if err != nil {
			return nil, err
		}
		payload := make([]byte, length+2)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, err
		}
		result = append(result, string(payload[:length]))
	}
	return result, nil
}

func equalCommand(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !strings.EqualFold(left[index], right[index]) {
			return false
		}
	}
	return true
}

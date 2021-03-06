/*
Adapted from https://github.com/golang/groupcache/blob/master/http_test.go
*/

package gcgrpcpool

import (
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang/groupcache"
	"google.golang.org/grpc"
)

var (
	peerAddrs = flag.String("test_peer_addrs", "", "Comma-separated list of peer addresses; used by TestGRPCPool")
	peerIndex = flag.Int("test_peer_index", -1, "Index of which peer this child is; used by TestGRPCPool")
	peerChild = flag.Bool("test_peer_child", false, "True if running as a child process; used by TestGRPCPool")
)

func TestGRPCPool(t *testing.T) {
	if *peerChild {
		beChildForTestGRPCPool()
		os.Exit(0)
	}

	const (
		nChild = 5
		nGets  = 5000
	)

	var childAddr []string
	for i := 0; i < nChild; i++ {
		childAddr = append(childAddr, pickFreeAddr(t))
	}

	var cmds []*exec.Cmd
	var wg sync.WaitGroup
	for i := 0; i < nChild; i++ {
		cmd := exec.Command(os.Args[0],
			"--test.run=TestGRPCPool",
			"--test_peer_child",
			"--test_peer_addrs="+strings.Join(childAddr, ","),
			"--test_peer_index="+strconv.Itoa(i),
		)
		cmds = append(cmds, cmd)
		wg.Add(1)
		if err := cmd.Start(); err != nil {
			t.Fatal("failed to start child process: ", err)
		}
		go awaitAddrReady(t, childAddr[i], &wg)
	}
	defer func() {
		for i := 0; i < nChild; i++ {
			if cmds[i].Process != nil {
				cmds[i].Process.Kill()
			}
		}
	}()
	wg.Wait()

	// Use a dummy self address so that we don't handle gets in-process.
	p := NewGRPCPool("should-be-ignored", grpc.NewServer())
	p.Set(childAddr...)

	// Dummy getter function. Gets should go to children only.
	// The only time this process will handle a get is when the
	// children can't be contacted for some reason.
	getter := groupcache.GetterFunc(func(ctx groupcache.Context, key string, dest groupcache.Sink) error {
		return errors.New("parent getter called; something's wrong")
	})
	g := groupcache.NewGroup("grpcPoolTest", 1<<20, getter)

	for _, key := range testKeys(nGets) {
		var value string
		if err := g.Get(nil, key, groupcache.StringSink(&value)); err != nil {
			t.Fatal(err)
		}
		if suffix := ":" + key; !strings.HasSuffix(value, suffix) {
			t.Errorf("Get(%q) = %q, want value ending in %q", key, value, suffix)
		}
		t.Logf("Get key=%q, value=%q (peer:key)", key, value)
	}
}

func testKeys(n int) (keys []string) {
	keys = make([]string, n)
	for i := range keys {
		keys[i] = strconv.Itoa(i)
	}
	return
}

func beChildForTestGRPCPool() {
	addrs := strings.Split(*peerAddrs, ",")
	server := grpc.NewServer()

	p := NewGRPCPool(addrs[*peerIndex], server)
	p.Set(addrs...)

	getter := groupcache.GetterFunc(func(ctx groupcache.Context, key string, dest groupcache.Sink) error {
		dest.SetString(strconv.Itoa(*peerIndex) + ":" + key)
		return nil
	})
	groupcache.NewGroup("grpcPoolTest", 1<<20, getter)
	lis, err := net.Listen("tcp", addrs[*peerIndex])
	if err != nil {
		log.Fatalf("Failed to listen on %s", addrs[*peerIndex])
	}

	server.Serve(lis)
}

func pickFreeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func awaitAddrReady(t *testing.T, addr string, wg *sync.WaitGroup) {
	defer wg.Done()
	const max = 1 * time.Second
	tries := 0
	for {
		tries++
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			return
		}
		delay := time.Duration(tries) * 25 * time.Millisecond
		if delay > max {
			delay = max
		}
		time.Sleep(delay)
	}
}

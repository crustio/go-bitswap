package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	cid "github.com/ipfs/go-cid"
)

const TimestampFormat = "2006-01-02T15:04:05-0700"

func CheckLogFileInvariants(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	logs := make([]map[string]interface{}, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var log map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &log); err != nil {
			return err
		}
		// fmt.Println(scanner.Text())

		t, err := time.Parse(TimestampFormat, log["ts"].(string))
		if err != nil {
			return err
		}
		log["ts"] = t
		logs = append(logs, log)
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return CheckInvariants(logs)
}

func CheckInvariants(logs []map[string]interface{}) error {
	if err := checkBlockReceivedOncePerSession(logs); err != nil {
		return err
	}

	if err := checkWantBlockCancel(logs); err != nil {
		return err
	}

	return nil
}

func checkBlockReceivedOncePerSession(logs []map[string]interface{}) error {
	peerSessions := make(map[string]*cid.Set)
	for _, log := range logs {
		if !strings.Contains(log["msg"].(string), "Bitswap <- block") {
			continue
		}

		peerSess := log["local"].(string) + "-" + strconv.Itoa(int(log["session"].(float64)))
		cids, ok := peerSessions[peerSess]
		c, err := cid.Parse(log["cid"])
		if err != nil {
			return err
		}

		if ok {
			if cids.Has(c) {
				return fmt.Errorf("Block %s received by peer-session %s twice", c, peerSess)
			}
		} else {
			cids = cid.NewSet()
			peerSessions[peerSess] = cids
		}
		cids.Add(c)
		// fmt.Println(peerSess, c)
	}
	return nil
}

type lineAt struct {
	line int
	at   time.Time
}

func checkWantBlockCancel(logs []map[string]interface{}) error {
	// node id => cid => timestamp
	rcvdBlocks := make(map[string]map[string]*lineAt)
	for lineIdx, log := range logs {
		if !strings.Contains(log["msg"].(string), "Bitswap <- block") {
			continue
		}

		pid := log["local"].(string)
		at := log["ts"].(time.Time)
		c := log["cid"].(string)

		peerBlks, ok := rcvdBlocks[pid]
		if !ok {
			peerBlks = make(map[string]*lineAt)
			rcvdBlocks[pid] = peerBlks
		}

		blkAt, ok := peerBlks[c]
		if !ok || at.Before(blkAt.at) {
			peerBlks[c] = &lineAt{lineIdx + 1, at}
		}
		// fmt.Println(log)
	}

	// node id => peer id => cid => timestamp
	nodePeerCidAt := func(typ string) map[string]map[string]map[string]*lineAt {
		res := make(map[string]map[string]map[string]*lineAt)
		for lineIdx, log := range logs {
			if !strings.Contains(log["msg"].(string), "sent message") {
				continue
			}
			if _, ok := log["type"]; !ok {
				continue
			}
			if !strings.HasPrefix(log["type"].(string), typ) {
				continue
			}

			localNode := log["local"].(string)
			at := log["ts"].(time.Time)
			c := log["cid"].(string)
			to := log["to"].(string)

			byNode, ok := res[localNode]
			if !ok {
				byNode = make(map[string]map[string]*lineAt)
				res[localNode] = byNode
			}

			byPeer, ok := byNode[to]
			if !ok {
				byPeer = make(map[string]*lineAt)
				byNode[to] = byPeer
			}
			byPeer[c] = &lineAt{lineIdx + 1, at}
			// fmt.Println(log)
		}
		return res
	}

	// node id => peer id => cid => timestamp
	sentWants := nodePeerCidAt("WANT")
	sentCancels := nodePeerCidAt("CANCEL")

	// Check if a want was sent to any peer after receiving the corresponding block
	for localNode, blkAt := range rcvdBlocks {
		for blk, rcvdAt := range blkAt {
			for wantTo, peerWants := range sentWants[localNode] {
				if wantAt, ok := peerWants[blk]; ok {
					if wantAt.at.After(rcvdAt.at) {
						msg := "Line %d: %s -> %s want %s: should not send want after receiving block "
						msg += "(Line %d: rcv %s)"
						return fmt.Errorf(msg, wantAt.line, localNode, wantTo, blk, rcvdAt.line, blk)
					}
				}
			}
		}
	}

	// Check that when a block was received, cancel was sent to each peer to
	// whom the want was sent
	for localNode, blkAt := range rcvdBlocks {
		for blk, rcvdAt := range blkAt {
			for wantTo, peerWants := range sentWants[localNode] {
				if _, ok := peerWants[blk]; ok {
					if _, ok := sentCancels[localNode][wantTo][blk]; !ok {
						return fmt.Errorf("Cancel was not sent to %s after receiving block %s (Line %d)", wantTo, blk, rcvdAt.line)
					}
				}
			}
		}
	}

	return nil
}

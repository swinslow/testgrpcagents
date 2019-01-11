// SPDX-License-Identifier: Apache-2.0 OR GPL-2.0-or-later
package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	pb "github.com/swinslow/testgrpcagents/agent"
	"google.golang.org/grpc"
)

type agentRef struct {
	name    string
	address string
}

type jobConfig struct {
	job     uint64
	dirPath string
	outPath string
	n       *sync.WaitGroup
	rc      chan<- jobResult
}

type jobResult struct {
	job uint64
	err error
}

func getAgents() []agentRef {
	// define known agents
	// in the actual version this should happen via service discovery
	agentHash := agentRef{name: "agenthash", address: "localhost:9001"}
	agentLen := agentRef{name: "agentlen", address: "localhost:9002"}
	return []agentRef{
		agentHash,
		agentLen,
	}
}

func runAgent(ag agentRef, cfg *jobConfig) {
	defer cfg.n.Done()

	var result jobResult
	result.job = cfg.job

	// connect and get client for each agent server
	conn, err := grpc.Dial(ag.address, grpc.WithInsecure())
	if err != nil {
		result.err = fmt.Errorf("could not connect to %s (%s): %v", ag.name, ag.address, err)
		cfg.rc <- result
		return
	}
	defer conn.Close()
	c := pb.NewAgentClient(conn)

	// set up context
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	// make server call to start job
	startReq := &pb.JobStartRequest{
		JobID:              cfg.job,
		DirectoryToAnalyze: cfg.dirPath,
		JobOutputFilename:  cfg.outPath,
	}
	log.Printf("== controller SEND JobStartRequest: JobID %d, DirectoryToAnalyze %s, JobOutputFilename %s", startReq.JobID, startReq.DirectoryToAnalyze, startReq.JobOutputFilename)
	startResp, err := c.JobStart(ctx, startReq)
	if err != nil {
		result.err = fmt.Errorf("could not start job for %s (%s): %v", ag.name, ag.address, err)
		cfg.rc <- result
		return
	}

	// if we get here, JobStart completed and we got a response
	// print a debug message
	log.Printf("== controller RECV JobStartResponse: JobID %d, WillStart %t, WillStartStatus %s\n", startResp.JobID, startResp.WillStart, startResp.WillStartStatus)

	// check back in 5 seconds and see if it's done
	time.Sleep(time.Second * 5)
	statusReq := &pb.JobStatusRequest{JobID: cfg.job}
	log.Printf("== controller SEND JobStatusRequest: JobID %d", statusReq.JobID)
	statusResp, err := c.JobStatus(ctx, statusReq)
	if err != nil {
		result.err = fmt.Errorf("could not get job status for %s (%s): %v", ag.name, ag.address, err)
		cfg.rc <- result
		return
	}
	// print a debug message
	log.Printf("== controller RECV JobStatusResponse: JobID %d, JobStatus %s, SecondsElapsed %d\n", statusResp.JobID, statusResp.JobStatus, statusResp.SecondsElapsed)

	// check status; even if not done, we'll cut and finish here
	if statusResp.JobStatus != "DONE" {
		result.err = fmt.Errorf("got job status %s for %s (%s)", statusResp.JobStatus, ag.name, ag.address)
		cfg.rc <- result
		return
	}

	// status was done! return successful result back to channel
	cfg.rc <- result
}

func getOutPath(job uint64) string {
	return fmt.Sprintf("/tmp/report%d.txt", job)
}

func main() {
	agents := getAgents()
	results := make(map[uint64]error)
	var jobID uint64 = 1

	dirPath := "/home/steve/programming/go/src/github.com/swinslow/testgrpcagents"

	// create one channel for all agents to respond on
	// FIXME should this be buffered or unbuffered?
	rc := make(chan jobResult)

	// create the waitgroup; we'll add one for each agent when we start it below
	var n sync.WaitGroup

	for _, ag := range agents {
		// build job config
		outPath := getOutPath(jobID)
		n.Add(1)
		jobCfg := &jobConfig{
			job:     jobID,
			dirPath: dirPath,
			outPath: outPath,
			n:       &n,
			rc:      rc,
		}
		jobID++

		// start each agent in a separate goroutine
		go runAgent(ag, jobCfg)
	}

	// now, start a separate goroutine to wait on the waitgroup until all
	// agents are done, and then close the response channel
	go func() {
		n.Wait()
		close(rc)
	}()

	// now, we can run a select loop on the channel, and when it is closed
	// we'll know that all Agents are done. we could also include a case
	// option for a periodic ticker if we want to check status.
	// FIXME but how would this work if we want to cancel an Agent early?
	// FIXME does that have to happen through the ctx / cancel within the Agent?
loop:
	for {
		select {
		case r, ok := <-rc:
			if !ok {
				// rc was closed; end the loop
				break loop
			}
			// add this agent's response to the map
			results[r.job] = r.err
		}
	}

	// loop is over because rc was closed
	for job, err := range results {
		if err == nil {
			fmt.Printf("Job %d result: see %s\n", job, getOutPath(job))
		} else {
			fmt.Printf("Job %d result: error: %v\n", job, err)
		}
	}
}

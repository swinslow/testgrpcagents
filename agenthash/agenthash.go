// SPDX-License-Identifier: Apache-2.0 OR GPL-2.0-or-later
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"github.com/swinslow/testgrpcagents/util"

	pb "github.com/swinslow/testgrpcagents/agent"
	"google.golang.org/grpc"
)

const (
	port = ":9001"
)

type jobStatus struct {
	status  string
	started time.Time
	ended   time.Time
}

type agentHash struct {
	jobs map[uint64]jobStatus
}

func (ag *agentHash) HealthCheck(ctx context.Context, healthReq *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	return nil, nil
}

func (ag *agentHash) JobStart(ctx context.Context, startReq *pb.JobStartRequest) (*pb.JobStartResponse, error) {
	ag.jobs[startReq.JobID] = jobStatus{status: "STARTING", started: time.Now()}
	// start the agent
	go ag.runAgent(startReq.JobID, startReq.DirectoryToAnalyze, startReq.JobOutputFilename)
	// and tell the caller we've started it
	return &pb.JobStartResponse{
		JobID:           startReq.JobID,
		WillStart:       true,
		WillStartStatus: "STARTING",
	}, nil
}

func (ag *agentHash) JobStatus(ctx context.Context, statusReq *pb.JobStatusRequest) (*pb.JobStatusResponse, error) {
	// look up job status
	jobID := statusReq.JobID
	status, prs := ag.jobs[jobID]
	if !prs {
		return &pb.JobStatusResponse{
			JobID:          jobID,
			JobStatus:      fmt.Sprintf("ERROR job ID %d not found", jobID),
			SecondsElapsed: 0,
		}, nil
	}

	// determine time elapsed, since now if still starting/running or
	// since end if errored / ended
	var elapsed uint64
	if status.status == "STARTING" || status.status == "RUNNING" {
		elapsed = (uint64)(time.Now().Sub(status.started).Seconds())
	} else {
		elapsed = (uint64)(status.ended.Sub(status.started).Seconds())
	}

	return &pb.JobStatusResponse{
		JobID:          jobID,
		JobStatus:      status.status,
		SecondsElapsed: elapsed,
	}, nil
}

func (ag *agentHash) JobCancel(ctx context.Context, cancelReq *pb.JobCancelRequest) (*pb.JobCancelResponse, error) {
	return nil, nil
}

func (ag *agentHash) runAgent(jobID uint64, dirPath string, outPath string) {
	status, prs := ag.jobs[jobID]
	if !prs {
		// job not found; log as error and return
		ag.jobs[jobID] = jobStatus{status: fmt.Sprintf("ERROR job ID %d not found in runAgent", jobID)}
	}
	ag.jobs[jobID] = jobStatus{status: "RUNNING", started: status.started}

	// pause for a moment so some time will elapse
	time.Sleep(time.Second * 2)

	// get slice with all relevant filenames in directory
	files, err := util.GetFileTreeSlice(dirPath)
	if err != nil {
		ag.jobs[jobID] = jobStatus{
			status:  fmt.Sprintf("ERROR couldn't get file tree for %s: %v", dirPath, err),
			started: status.started,
			ended:   time.Now(),
		}
		return
	}
	files = util.FilterFileTreeSlice(files)

	// open the output file for writing
	outFile, err := os.Create(outPath)
	if err != nil {
		ag.jobs[jobID] = jobStatus{
			status:  fmt.Sprintf("ERROR couldn't open %s for writing: %v", outPath, err),
			started: status.started,
			ended:   time.Now(),
		}
		return
	}

	// now calculate hash for each file
	for _, filename := range files {
		fmt.Fprintf(outFile, "%s: ", filename)
		// read file and calculate hash
		f, err := os.Open(filename)
		if err != nil {
			fmt.Fprintf(outFile, "ERROR couldn't open file: %v", err)
		} else {
			// don't defer f.Close() here, b/c we're in a loop;
			// make sure we actually close below
			hSha256 := sha256.New()
			if _, err = io.Copy(hSha256, f); err != nil {
				fmt.Fprintf(outFile, "ERROR couldn't calculate hash: %v", err)
			} else {
				fmt.Fprintf(outFile, "%x", hSha256.Sum(nil))
			}
			f.Close()
		}
		fmt.Fprintf(outFile, "\n")
	}

	// we're done!
	outFile.Close()
	ag.jobs[jobID] = jobStatus{
		status:  "DONE",
		started: status.started,
		ended:   time.Now(),
	}
}

func main() {
	// open a socket for listening
	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("couldn't open port %v: %v", port, err)
	}

	// create and register new GRPC server for agent
	server := grpc.NewServer()
	pb.RegisterAgentServer(server, &agentHash{
		jobs: make(map[uint64]jobStatus),
	})

	// FIXME is reflection needed? If so, for what?

	// start grpc server
	if err := server.Serve(lis); err != nil {
		log.Fatalf("couldn't start server: %v", err)
	}
}

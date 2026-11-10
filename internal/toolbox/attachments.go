package toolbox

import (
	"context"
	"encoding/json"
	"errors"
)

func listAttachments(ctx context.Context, deps Deps, args json.RawMessage) (Result, error) {
	var request struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.Unmarshal(args, &request); err != nil {
		return Result{}, err
	}
	if request.TaskID <= 0 {
		return Result{}, errors.New("task_id is required")
	}
	task, err := deps.Store.TaskByID(ctx, request.TaskID)
	if err != nil {
		return Result{}, err
	}
	if task == nil {
		return Result{}, errors.New("task not found")
	}
	member, err := deps.Store.IsMember(ctx, task.ProjectID, deps.ActorID)
	if err != nil {
		return Result{}, err
	}
	if !member {
		return Result{}, errors.New("task not found")
	}
	attachments, err := deps.Store.ListAttachments(ctx, task.ID)
	if err != nil {
		return Result{}, err
	}
	return Result{Ok: true, Data: attachments}, nil
}

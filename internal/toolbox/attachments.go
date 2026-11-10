package toolbox

import (
	"context"
	"encoding/json"
	"errors"
)

func listAttachments(ctx context.Context, deps Deps, _ json.RawMessage) (Result, error) {
	taskID, err := authorizedTaskID(ctx)
	if err != nil {
		return Result{}, err
	}
	task, err := deps.Store.TaskByID(ctx, taskID)
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

package trello

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var boardIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{8,24}$`)

type List struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Card struct {
	ID     string     `json:"id"`
	ListID string     `json:"idList"`
	Name   string     `json:"name"`
	Desc   string     `json:"desc"`
	Due    *time.Time `json:"due"`
}

type Board struct {
	ID    string `json:"id"`
	Lists []List `json:"-"`
	Cards []Card `json:"-"`
}

type Client struct {
	BaseURL string
	Key     string
	Token   string
	HTTP    *http.Client
}

func New(key, token string) *Client {
	return &Client{
		BaseURL: "https://api.trello.com/1",
		Key:     key,
		Token:   token,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *Client) FetchBoard(ctx context.Context, boardID string) (*Board, error) {
	if !boardIDPattern.MatchString(boardID) {
		return nil, errors.New("укажи идентификатор доски Trello")
	}
	if c.Key == "" || c.Token == "" || strings.ContainsAny(c.Key+c.Token, "\r\n\"") {
		return nil, errors.New("Trello не подключён на сервере")
	}
	board := &Board{}
	base := "/boards/" + url.PathEscape(boardID)
	if err := c.get(ctx, base+"?fields=id", board); err != nil {
		return nil, err
	}
	if !boardIDPattern.MatchString(board.ID) {
		return nil, errors.New("Trello вернул некорректную доску")
	}
	base = "/boards/" + url.PathEscape(board.ID)
	if err := c.get(ctx, base+"/lists?filter=open&fields=id,name", &board.Lists); err != nil {
		return nil, err
	}
	if err := c.get(ctx, base+"/cards?filter=open&fields=id,idList,name,desc,due", &board.Cards); err != nil {
		return nil, err
	}
	return board, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return errors.New("не удалось подготовить запрос к Trello")
	}
	req.Header.Set("Authorization", fmt.Sprintf(`OAuth oauth_consumer_key="%s", oauth_token="%s"`, c.Key, c.Token))
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("не удалось связаться с Trello")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Trello вернул ошибку %d", resp.StatusCode)
	}
	const maxResponse = 16 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return errors.New("не удалось прочитать ответ Trello")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("Trello вернул некорректный ответ")
	}
	return nil
}

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
	"strconv"
	"strings"
	"time"
)

var boardIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{8,24}$`)
var cardIDPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)

const cardPageSize = 1000
const maxCardPages = 10000

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
	if board.Lists == nil {
		return nil, errors.New("Trello вернул некорректный ответ со списками")
	}
	cards, err := c.fetchCards(ctx, base)
	if err != nil {
		return nil, err
	}
	board.Cards = cards
	return board, nil
}

// Trello's before cursor is a creation timestamp, with only second precision
// in card IDs. Overlap the boundary second so cards created together are not
// dropped; reject a page that cannot advance rather than return partial data.
func (c *Client) fetchCards(ctx context.Context, base string) ([]Card, error) {
	cards := []Card{}
	seen := map[string]bool{}
	var before time.Time
	for pageNumber := 0; pageNumber < maxCardPages; pageNumber++ {
		query := url.Values{"filter": {"open"}, "fields": {"id,idList,name,desc,due"}, "limit": {strconv.Itoa(cardPageSize)}, "sort": {"-id"}}
		if !before.IsZero() {
			query.Set("before", before.Format(time.RFC3339))
		}
		var page []Card
		if err := c.get(ctx, base+"/cards?"+query.Encode(), &page); err != nil {
			return nil, err
		}
		if page == nil {
			return nil, errors.New("Trello вернул некорректный ответ с карточками")
		}
		if len(page) > cardPageSize {
			return nil, errors.New("Trello вернул слишком много карточек на странице")
		}
		// Short final pages cannot require another cursor; existing tests and
		// clients can therefore keep using synthetic IDs for small boards.
		if len(page) < cardPageSize && before.IsZero() {
			return page, nil
		}
		var oldest time.Time
		for index, card := range page {
			if !cardIDPattern.MatchString(card.ID) || (index > 0 && page[index-1].ID <= card.ID) {
				return nil, errors.New("Trello вернул некорректный порядок карточек")
			}
			seconds, _ := strconv.ParseInt(card.ID[:8], 16, 64)
			created := time.Unix(seconds, 0).UTC()
			if !before.IsZero() && !created.Before(before) {
				return nil, errors.New("Trello вернул некорректную страницу карточек")
			}
			oldest = created
			if !seen[card.ID] {
				seen[card.ID] = true
				cards = append(cards, card)
			}
		}
		if len(page) < cardPageSize {
			return cards, nil
		}
		next := oldest.Add(time.Second)
		if !before.IsZero() && !next.Before(before) {
			return nil, errors.New("Trello не позволяет продолжить загрузку карточек")
		}
		before = next
	}
	return nil, errors.New("превышен предел страниц Trello")
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

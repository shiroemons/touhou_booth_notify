package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/api/bsky"
	lexutil "github.com/bluesky-social/indigo/lex/util"
	"github.com/bluesky-social/indigo/util"
	"github.com/bluesky-social/indigo/xrpc"
	"github.com/bwmarrin/discordgo"
	"github.com/dghubble/go-twitter/twitter"
	"github.com/dghubble/oauth1"
	"github.com/gocolly/colly"
	"github.com/joho/godotenv"
	"github.com/shopspring/decimal"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

type NotifyParams struct {
	tCli      *twitter.Client
	dCli      *discordgo.Session
	bCli      *xrpc.Client
	channelID string
}

type Item struct {
	bun.BaseModel `bun:"table:items,alias:i"`

	ID        int64     `bun:"id,pk,autoincrement"`
	Name      string    `bun:"name,notnull"`
	Category  string    `bun:"category,notnull,default:''"`
	Price     string    `bun:"price,type:numeric,notnull"`
	URL       string    `bun:"url,notnull"`
	ImageURL  string    `bun:"image_url,notnull"`
	ShopName  string    `bun:"-"`
	CreatedAt time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}

func envLoad() {
	if os.Getenv("GO_ENV") == "" {
		err := os.Setenv("GO_ENV", "development")
		if err != nil {
			return
		}
	}
	if os.Getenv("GO_ENV") != "production" {
		fileName := fmt.Sprintf(".env.%s", os.Getenv("GO_ENV"))
		if err := godotenv.Load(fileName); err != nil {
			log.Fatal("Error loading .env file")
		}
	}
}

func mustGetenv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("Warning: %s environment variable not set.", k)
	}
	return v
}

func setupTwitterClient() *twitter.Client {
	var (
		consumerKey       = os.Getenv("TWITTER_CONSUMER_KEY")
		consumerSecret    = os.Getenv("TWITTER_CONSUMER_SECRET")
		accessToken       = os.Getenv("TWITTER_ACCESS_TOKEN")
		accessTokenSecret = os.Getenv("TWITTER_ACCESS_TOKEN_SECRET")
	)
	if consumerKey == "" || consumerSecret == "" || accessToken == "" || accessTokenSecret == "" {
		return nil
	}

	// Twitter client setup
	config := oauth1.NewConfig(consumerKey, consumerSecret)
	token := oauth1.NewToken(accessToken, accessTokenSecret)
	httpClient := config.Client(oauth1.NoContext, token)

	return twitter.NewClient(httpClient)
}

func setupDiscord() *discordgo.Session {
	token := os.Getenv("DISCORD_BOT_TOKEN")
	if token == "" {
		return nil
	}

	discord, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("error discord setup: %s", err)
	}
	return discord
}

func setupBluesky(ctx context.Context) *xrpc.Client {
	cli := &xrpc.Client{
		Host: "https://bsky.social",
	}

	identifier := os.Getenv("BLUESKY_HANDLE")
	password := os.Getenv("BLUESKY_PASSWORD")
	input := &atproto.ServerCreateSession_Input{
		Identifier: identifier,
		Password:   password,
	}
	output, err := atproto.ServerCreateSession(ctx, cli, input)
	if err != nil {
		log.Fatal(err)
	}
	cli.Auth = &xrpc.AuthInfo{
		AccessJwt:  output.AccessJwt,
		RefreshJwt: output.RefreshJwt,
		Handle:     output.Handle,
		Did:        output.Did,
	}

	return cli
}

func setupDB(ctx context.Context) *bun.DB {
	dsn := mustGetenv("DATABASE_DSN")

	// Database
	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))
	db := bun.NewDB(sqldb, pgdialect.New())

	var v string
	if err := db.NewSelect().ColumnExpr("version()").Scan(ctx, &v); err != nil {
		panic(err)
	}
	log.Println(v)

	return db
}

var (
	debug bool
	_     bun.BeforeAppendModelHook = (*Item)(nil)
)

func init() {
	envLoad()
}

func main() {
	log.Println("touhou booth notify start!")
	debug = os.Getenv("DEBUG") != ""

	ctx := context.Background()

	db := setupDB(ctx)
	// Twitter client
	tClient := setupTwitterClient()
	// Discord client
	discord := setupDiscord()
	err := discord.Open()
	if err != nil {
		log.Fatalf("error opening connection: %s", err)
	}
	defer discord.Close()
	// Bluesky client
	bClient := setupBluesky(ctx)

	params := NotifyParams{
		tCli:      tClient,
		dCli:      discord,
		bCli:      bClient,
		channelID: os.Getenv("DISCORD_CHANNEL_ID"),
	}

	items, err := getItems()
	if err != nil {
		log.Fatalf("getItems error: %s", err)
	}

	for i := len(items) - 1; i >= 0; i-- {
		if debug {
			if i == 0 {
				run(ctx, db, items[i], params)
			}
		} else {
			run(ctx, db, items[i], params)
		}
	}

	log.Println("touhou booth notify successfully completed!")
}

func (i *Item) BeforeAppendModel(_ context.Context, query bun.Query) error {
	switch query.(type) {
	case *bun.InsertQuery:
		now := time.Now()
		i.CreatedAt = now
		i.UpdatedAt = now
	case *bun.UpdateQuery:
		i.UpdatedAt = time.Now()
	}
	return nil
}

func getItems() ([]*Item, error) {
	baseURL := "https://booth.pm/ja/browse/%E9%9F%B3%E6%A5%BD?in_stock=true&new_arrival=true&q=%E6%9D%B1%E6%96%B9Project&sort=new&type=digital"
	c := colly.NewCollector()

	var items []*Item
	c.OnHTML("li.item-card", func(e *colly.HTMLElement) {
		category := e.DOM.Find("div.item-card__category").Text()
		name := e.DOM.Find("div.item-card__title").Text()
		shopName := e.DOM.Find("div.item-card__shop-name").Text()
		price := e.Attr("data-product-price") + ".0"
		url, _ := e.DOM.Find("div.item-card__title a").Attr("href")
		imageURL, _ := e.DOM.Find("div img").Attr("src")

		if strings.HasPrefix("楽譜", shopName) {
			return
		}

		item := &Item{
			Category: category,
			Name:     name,
			ShopName: shopName,
			Price:    price,
			URL:      url,
			ImageURL: imageURL,
		}
		items = append(items, item)
	})

	err := c.Visit(baseURL)
	if err != nil {
		return nil, err
	}

	return items, nil
}

func run(ctx context.Context, db *bun.DB, item *Item, p NotifyParams) {
	dbItem := itemFindByURL(ctx, db, item.URL)

	if debug {
		title := fmt.Sprintf("【テスト】【🆕新着情報🆕】 %s - %s", item.ShopName, item.Name)
		msg := fmt.Sprintf("【テスト】【🆕新着情報🆕】\n\n%s\n%s\n%s円\n\n%s\n%s",
			item.Category,
			item.Name,
			decimal.RequireFromString(item.Price),
			item.URL,
			item.ShopName,
		)

		notify(ctx, p, title, msg)
	} else if dbItem.ID == 0 {
		if err := insert(ctx, db, item); err != nil {
			return
		}

		title := fmt.Sprintf("【🆕新着情報🆕】 %s - %s", item.ShopName, item.Name)
		msg := fmt.Sprintf("【🆕新着情報🆕】\n\n%s\n%s\n%s円\n\n%s\n%s",
			item.Category,
			item.Name,
			decimal.RequireFromString(item.Price),
			item.URL,
			item.ShopName,
		)

		notify(ctx, p, title, msg)
	} else if item.Price != dbItem.Price {
		oldPrice := decimal.RequireFromString(dbItem.Price)
		newPrice := decimal.RequireFromString(item.Price)
		dbItem.Price = item.Price
		if err := update(ctx, db, dbItem); err != nil {
			return
		}

		title := fmt.Sprintf("【🆙更新情報🆙】 %s - %s", item.ShopName, item.Name)
		msg := fmt.Sprintf("【🆙更新情報🆙】\n\n%s\n%s\n%s円 -> %s円\n\n%s\n%s",
			item.Category,
			item.Name,
			oldPrice,
			newPrice,
			item.URL,
			item.ShopName,
		)

		notify(ctx, p, title, msg)
	}
}

func itemFindByURL(ctx context.Context, db *bun.DB, url string) *Item {
	dbItem := new(Item)
	_ = db.NewSelect().Model(dbItem).Where("url = ?", url).Scan(ctx)

	return dbItem
}

func insert(ctx context.Context, db *bun.DB, item *Item) error {
	_, err := db.NewInsert().Model(item).Exec(ctx)
	if err != nil {
		fmt.Println(err)
		return err
	}
	return nil
}

func update(ctx context.Context, db *bun.DB, item *Item) error {
	_, err := db.NewUpdate().Model(item).WherePK().Exec(ctx)
	if err != nil {
		fmt.Println(err)
		return err
	}
	return nil
}

func notify(ctx context.Context, p NotifyParams, title, msg string) {
	if p.tCli != nil && !debug {
		tweet(p.tCli, msg+"\n\n#booth_pm #東方デジタル音楽\n#東方Project #東方楽曲 #東方アレンジ")
	}
	if p.dCli != nil && p.channelID != "" {
		sendMessage(p.dCli, p.channelID, msg)
	}
	if p.bCli != nil {
		postBluesky(ctx, p.bCli, msg)
	}
}

func tweet(cli *twitter.Client, msg string) {
	_, _, err := cli.Statuses.Update(msg, nil)
	if err != nil {
		log.Printf("tweet error: %s", err)
	}
}

func sendMessage(s *discordgo.Session, channelID, msg string) {
	_, err := s.ChannelMessageSend(channelID, msg)
	if err != nil {
		log.Println("Error sending message: ", err)
	}
}

func postBluesky(ctx context.Context, cli *xrpc.Client, msg string) {
	input := &atproto.RepoCreateRecord_Input{
		Collection: "app.bsky.feed.post",
		Repo:       cli.Auth.Did,
		Record: &lexutil.LexiconTypeDecoder{
			Val: &bsky.FeedPost{
				Text:      msg,
				CreatedAt: time.Now().Format(util.ISO8601),
				Langs:     []string{"ja"},
				Tags: []string{
					"booth_pm",
					"東方デジタル音楽",
					"東方Project",
					"東方楽曲",
					"東方アレンジ",
				},
			},
		},
	}

	_, err := atproto.RepoCreateRecord(ctx, cli, input)
	if err != nil {
		log.Println("Error posting to bluesky: ", err)
	}
}

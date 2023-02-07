package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/dghubble/go-twitter/twitter"
	"github.com/dghubble/oauth1"
	"github.com/gocolly/colly"
	"github.com/joho/godotenv"
	"github.com/mattn/go-mastodon"
	"github.com/shopspring/decimal"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

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
		consumerKey       = mustGetenv("TWITTER_CONSUMER_KEY")
		consumerSecret    = mustGetenv("TWITTER_CONSUMER_SECRET")
		accessToken       = mustGetenv("TWITTER_ACCESS_TOKEN")
		accessTokenSecret = mustGetenv("TWITTER_ACCESS_TOKEN_SECRET")
	)

	// Twitter client setup
	config := oauth1.NewConfig(consumerKey, consumerSecret)
	token := oauth1.NewToken(accessToken, accessTokenSecret)
	httpClient := config.Client(oauth1.NoContext, token)

	return twitter.NewClient(httpClient)
}

func setupMastodonClient(ctx context.Context) *mastodon.Client {
	var (
		serverURL    = mustGetenv("MASTODON_SERVER_URL")
		clientID     = mustGetenv("MASTODON_CLIENT_ID")
		clientSecret = mustGetenv("MASTODON_CLIENT_SECRET")
		email        = mustGetenv("MASTODON_EMAIL")
		password     = mustGetenv("MASTODON_PASSWORD")
	)

	c := mastodon.NewClient(&mastodon.Config{
		Server:       serverURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	})

	err := c.Authenticate(ctx, email, password)
	if err != nil {
		log.Fatalf("Error mastodon login: %s", err)
	}
	return c
}

func setupDiscord() *discordgo.Session {
	token := mustGetenv("DISCORD_BOT_TOKEN")
	discord, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatalf("error discord setup: %s", err)
	}
	return discord
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

func main() {
	log.Println("touhou booth notify start!")
	ctx := context.Background()
	envLoad()

	db := setupDB(ctx)
	// Twitter client
	tClient := setupTwitterClient()
	// Mastodon client
	mClient := setupMastodonClient(ctx)
	// Discord client
	discord := setupDiscord()
	err := discord.Open()
	if err != nil {
		log.Fatalf("error opening connection: %s", err)
	}
	defer discord.Close()

	items, err := getItems()
	if err != nil {
		log.Fatalf("getItems error: %s", err)
	}

	for i := len(items) - 1; i >= 0; i-- {
		run(ctx, db, items[i], tClient, mClient, discord)
	}
	log.Println("touhou booth notify successfully completed!")
}

var _ bun.BeforeAppendModelHook = (*Item)(nil)

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

func run(ctx context.Context, db *bun.DB, item *Item, tCli *twitter.Client, mCli *mastodon.Client, dCli *discordgo.Session) {
	channelID := mustGetenv("DISCORD_CHANNEL_ID")
	dbItem := itemFindByURL(ctx, db, item.URL)

	if dbItem.ID == 0 {
		if err := insert(ctx, db, item); err != nil {
			return
		}

		msg := fmt.Sprintf("【🆕新着情報🆕】\n\n%s\n%s\n%s円\n\n%s\n%s",
			item.Category,
			item.Name,
			decimal.RequireFromString(item.Price),
			item.URL,
			item.ShopName,
		)

		tweet(tCli, msg+"\n\n#booth_pm #東方デジタル音楽\n#東方Project #東方楽曲 #東方アレンジ")
		toot(ctx, mCli, msg)
		sendMessage(dCli, channelID, msg)
	} else if item.Price != dbItem.Price {
		oldPrice := decimal.RequireFromString(dbItem.Price)
		newPrice := decimal.RequireFromString(item.Price)
		dbItem.Price = item.Price
		if err := update(ctx, db, dbItem); err != nil {
			return
		}

		msg := fmt.Sprintf("【🆙更新情報🆙】\n\n%s\n%s\n%s円 -> %s円\n\n%s\n%s",
			item.Category,
			item.Name,
			oldPrice,
			newPrice,
			item.URL,
			item.ShopName,
		)

		tweet(tCli, msg+"\n\n#booth_pm #東方デジタル音楽\n#東方Project #東方楽曲 #東方アレンジ")
		toot(ctx, mCli, msg)
		sendMessage(dCli, channelID, msg)
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

func tweet(cli *twitter.Client, msg string) {
	_, _, err := cli.Statuses.Update(msg, nil)
	if err != nil {
		log.Printf("tweet error: %s", err)
	}
}

func toot(ctx context.Context, cli *mastodon.Client, msg string) {
	t := &mastodon.Toot{
		SpoilerText: msg,
	}
	_, err := cli.PostStatus(ctx, t)
	if err != nil {
		log.Printf("toot error: %s", err)
	}
}

func sendMessage(s *discordgo.Session, channelID, msg string) {
	_, err := s.ChannelMessageSend(channelID, msg)
	if err != nil {
		log.Println("Error sending message: ", err)
	}
}

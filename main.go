package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gocolly/colly"
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

func mustGetenv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("Warning: %s environment variable not set.", k)
	}
	return v
}

func main() {
	ctx := context.Background()

	dsn := mustGetenv("DSN")
	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))
	db := bun.NewDB(sqldb, pgdialect.New())

	var v string
	if err := db.NewSelect().ColumnExpr("version()").Scan(ctx, &v); err != nil {
		panic(err)
	}

	items, err := getItems()
	if err != nil {
		log.Fatalf("getItems error: %s", err)
	}

	for i := len(items) - 1; i >= 0; i-- {
		run(ctx, db, items[i])
	}
}

var _ bun.BeforeAppendModelHook = (*Item)(nil)

func (i *Item) BeforeAppendModel(ctx context.Context, query bun.Query) error {
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

func run(ctx context.Context, db *bun.DB, item *Item) {
	dbItem := itemFindByURL(ctx, db, item.URL)

	if dbItem.ID == 0 {
		if err := insert(ctx, db, item); err != nil {
			return
		}

		tweet := fmt.Sprintf("【🆕新着情報🆕】\n\n%s\n%s\n%s円\n\n%s\n%s\n\n#booth_pm #東方デジタル音楽\n#東方Project #東方楽曲 #東方アレンジ",
			item.Category,
			item.Name,
			item.Price,
			item.URL,
			item.ShopName,
		)
		fmt.Println(tweet)
	} else if item.Price != dbItem.Price {
		oldPrice := decimal.RequireFromString(dbItem.Price)
		newPrice := decimal.RequireFromString(item.Price)
		dbItem.Price = item.Price
		if err := update(ctx, db, dbItem); err != nil {
			return
		}

		tweet := fmt.Sprintf("【🆙更新情報🆙】\n\n%s\n%s\n%s円 -> %s円\n\n%s\n%s\n\n#booth_pm #東方デジタル音楽\n#東方Project #東方楽曲 #東方アレンジ",
			item.Category,
			item.Name,
			oldPrice,
			newPrice,
			item.URL,
			item.ShopName,
		)
		fmt.Println(tweet)
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

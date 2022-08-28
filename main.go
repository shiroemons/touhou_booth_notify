package main

import (
	"fmt"
	"strings"

	"github.com/gocolly/colly"
)

type Item struct {
	Category string
	Name     string
	ShopName string
	Price    string
	URL      string
	ImageURL string
}

func main() {
	baseURL := "https://booth.pm/ja/browse/%E9%9F%B3%E6%A5%BD?in_stock=true&new_arrival=true&q=%E6%9D%B1%E6%96%B9Project&sort=new&type=digital"

	c := colly.NewCollector()

	var items []*Item
	c.OnHTML("li.item-card", func(e *colly.HTMLElement) {
		category := e.DOM.Find("div.item-card__category").Text()
		name := e.DOM.Find("div.item-card__title").Text()
		shopName := e.DOM.Find("div.item-card__shop-name").Text()
		price := e.Attr("data-product-price")
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

	c.Visit(baseURL)

	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		tweet := fmt.Sprintf("【🆕新着情報🆕】\n\n%s\n%s\n%s円\n\n%s\n%s\n\n#booth_pm #東方デジタル音楽\n#東方Project #東方楽曲 #東方アレンジ",
			item.Category,
			item.Name,
			item.Price,
			item.URL,
			item.ShopName,
		)
		fmt.Println(tweet)
	}
}

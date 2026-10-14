package blog

import (
	"net/url"
	"strconv"
	"unicode/utf8"
)

// Independent columns prevent a tall category from leaving empty grid rows.
func publicBalancedColumns(groups []publicGroup) [][]publicGroup {
	if len(groups) == 0 {
		return nil
	}
	if len(groups) == 1 {
		return [][]publicGroup{groups}
	}
	columns := make([][]publicGroup, 2)
	heights := [2]int{}
	for _, group := range groups {
		column := 0
		if heights[1] < heights[0] {
			column = 1
		}
		columns[column] = append(columns[column], group)
		heights[column] += 80
		for _, card := range group.Cards {
			heights[column] += 130 + min(4, (utf8.RuneCountInString(string(card.HTML))+44)/45)*25
			if card.Image != "" {
				heights[column] += 165
			}
		}
	}
	return columns
}

func publicPaginationLinks(settings SiteSettings, lang string, query url.Values, page, total int) []publicPageLink {
	var links []publicPageLink
	previous := 0
	for number := 1; number <= total; number++ {
		if total > 9 && number != 1 && number != total && (number < page-2 || number > page+2) {
			continue
		}
		if number > previous+1 {
			links = append(links, publicPageLink{Label: "…"})
		}
		q := clonePublicQuery(query)
		q.Set("paged", strconv.Itoa(number))
		links = append(links, publicPageLink{Label: strconv.Itoa(number), URL: publicArchiveURL(settings, lang, q), Current: number == page})
		previous = number
	}
	return links
}

package model

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"net/http"

	"github.com/dingoblog/dingo/app/utils"
	"github.com/russross/meddler"
)

const stmtGetPostById = `SELECT * FROM posts WHERE id = ?`
const stmtGetPostBySlug = `SELECT * FROM posts WHERE slug = ?`
const stmtGetPostsByTag = `SELECT * FROM posts WHERE %s id IN ( SELECT post_id FROM posts_tags WHERE tag_id = ? ) ORDER BY published_at DESC LIMIT ? OFFSET ?`
const stmtGetAllPostsByTag = `SELECT * FROM posts WHERE id IN ( SELECT post_id FROM posts_tags WHERE tag_id = ?) ORDER BY published_at DESC `
const stmtGetPostsCountByTag = "SELECT count(*) FROM posts, posts_tags WHERE posts_tags.post_id = posts.id AND posts.published AND posts_tags.tag_id = ?"
const stmtInsertPostTag = `INSERT INTO posts_tags (id, post_id, tag_id) VALUES (?, ?, ?)`
const stmtDeletePostTagsByPostId = `DELETE FROM posts_tags WHERE post_id = ?`
const stmtNumberOfPosts = "SELECT count(*) FROM posts WHERE %s"
const stmtGetAllPostList = `SELECT * FROM posts WHERE %s ORDER BY %s`
const stmtGetPostList = `SELECT * FROM posts WHERE %s ORDER BY %s LIMIT ? OFFSET ?`
const stmtDeletePostById = `DELETE FROM posts WHERE id = ?`

var safeOrderByStmt = map[string]string{
	"created_at":        "created_at",
	"created_at DESC":   "created_at DESC",
	"updated_at":        "updated_at",
	"updated_at DESC":   "updated_at DESC",
	"published_at":      "published_at",
	"published_at DESC": "published_at DESC",
}

type Post struct {
	Id              int64      `meddler:"id,pk"`
	Title           string     `meddler:"title"`
	Slug            string     `meddler:"slug"`
	Markdown        string     `meddler:"markdown"`
	Html            string     `meddler:"html"`
	Image           string     `meddler:"image"`
	IsFeatured      bool       `meddler:"featured"`
	IsPage          bool       `meddler:"page"`
	AllowComment    bool       `meddler:"allow_comment"`
	CommentNum      int64      `meddler:"comment_num"`
	IsPublished     bool       `meddler:"published"`
	Language        string     `meddler:"language"`
	MetaTitle       string     `meddler:"meta_title"`
	MetaDescription string     `meddler:"meta_description"`
	CreatedAt       *time.Time `meddler:"created_at"`
	CreatedBy       int64      `meddler:"created_by"`
	UpdatedAt       *time.Time `meddler:"updated_at"`
	UpdatedBy       int64      `meddler:"updated_by"`
	PublishedAt     *time.Time `meddler:"published_at"`
	PublishedBy     int64      `meddler:"published_by"`
	Hits            int64      `meddler:"-"`
	Category        string     `meddler:"-"`
}

type Posts []*Post

func (p Posts) Len() int {
	return len(p)
}

func (p Posts) Get(i int) *Post {
	return p[i]
}

func NewPost() *Post {
	return &Post{
		CreatedAt: utils.Now(),
	}
}

func (p *Post) TagString() string {
	tags := new(Tags)
	_ = tags.GetTagsByPostId(p.Id)
	var tagString string
	for i := 0; i < tags.Len(); i++ {
		if i != tags.Len()-1 {
			tagString += tags.Get(i).Name + ", "
		} else {
			tagString += tags.Get(i).Name
		}
	}
	return tagString
}

func (p *Post) Url() string {
	return "/" + p.Slug
}

func (p *Post) Tags() []*Tag {
	tags := new(Tags)
	err := tags.GetTagsByPostId(p.Id)
	if err != nil {
		return nil
	}
	return tags.GetAll()
}

func (p *Post) Author() *User {
	user := &User{Id: p.CreatedBy}
	err := user.GetUserById()
	if err != nil {
		return ghostUser
	}
	return user
}

func (p *Post) Comments() []*Comment {
	comments := new(Comments)
	err := comments.GetCommentsByPostId(p.Id)
	if err != nil {
		return nil
	}
	return comments.GetAll()
}

func (p *Post) Summary() string {
	text := strings.Split(p.Markdown, "<!--more-->")[0]
	return utils.Markdown2Html(text)
}

func (p *Post) Excerpt() string {
	return utils.Html2Excerpt(p.Html, 255)
}

func (p *Post) Save(tags ...*Tag) error {
	p.Slug = strings.TrimLeft(p.Slug, "/")
	p.Slug = strings.TrimRight(p.Slug, "/")
	if p.Slug == "" {
		return fmt.Errorf("Slug can not be empty or root")
	}

	if p.IsPublished {
		p.PublishedAt = utils.Now()
		p.PublishedBy = p.CreatedBy
	}

	p.UpdatedAt = utils.Now()
	p.UpdatedBy = p.CreatedBy

	if p.Id == 0 {
		// Insert post
		if err := p.Insert(); err != nil {
			return err
		}
	} else {
		if err := p.Update(); err != nil {
			return err
		}
	}
	tagIds := make([]int64, 0)
	// Insert tags
	for _, t := range tags {
		t.CreatedAt = utils.Now()
		t.CreatedBy = p.CreatedBy
		t.Hidden = !p.IsPublished
		t.Save()
		tagIds = append(tagIds, t.Id)
	}
	// Delete old post-tag projections
	err := DeletePostTagsByPostId(p.Id)
	// Insert postTags
	if err != nil {
		return err
	}
	for _, tagId := range tagIds {
		err := InsertPostTag(p.Id, tagId)
		if err != nil {
			return err
		}
	}
	return DeleteOldTags()
}

func (p *Post) Insert() error {
	if !PostChangeSlug(p.Slug) {
		p.Slug = generateNewSlug(p.Slug, 1)
	}
	err := meddler.Insert(db, "posts", p)
	return err
}

func InsertPostTag(post_id int64, tag_id int64) error {
	writeDB, err := db.Begin()
	if err != nil {
		writeDB.Rollback()
		return err
	}
	_, err = writeDB.Exec(stmtInsertPostTag, nil, post_id, tag_id)
	if err != nil {
		writeDB.Rollback()
		return err
	}
	return writeDB.Commit()
}

func (p *Post) Update() error {
	currentPost := &Post{Id: p.Id}
	err := currentPost.GetPostById()
	if err != nil {
		return err
	}
	if p.Slug != currentPost.Slug && !PostChangeSlug(p.Slug) {
		p.Slug = generateNewSlug(p.Slug, 1)
	}
	err = meddler.Update(db, "posts", p)
	return err
}

func (p *Post) UpdateFromRequest(r *http.Request) {
	p.Title = r.FormValue("title")
	p.Image = r.FormValue("image")
	p.Slug = r.FormValue("slug")
	p.Markdown = r.FormValue("content")
	p.Html = utils.Markdown2Html(p.Markdown)
	p.AllowComment = r.FormValue("comment") == "on"
	p.Category = r.FormValue("category")
	p.IsPublished = r.FormValue("status") == "on"
}

func DeletePostTagsByPostId(post_id int64) error {
	writeDB, err := db.Begin()
	if err != nil {
		writeDB.Rollback()
		return err
	}
	_, err = writeDB.Exec(stmtDeletePostTagsByPostId, post_id)
	if err != nil {
		writeDB.Rollback()
		return err
	}
	return writeDB.Commit()
}

func DeletePostById(id int64) error {
	writeDB, err := db.Begin()
	if err != nil {
		writeDB.Rollback()
		return err
	}
	_, err = writeDB.Exec(stmtDeletePostById, id)
	if err != nil {
		writeDB.Rollback()
		return err
	}
	err = writeDB.Commit()
	if err != nil {
		return err
	}
	err = DeletePostTagsByPostId(id)
	if err != nil {
		return err
	}
	return DeleteOldTags()
}

func (post *Post) GetPostById() error {
	err := meddler.QueryRow(db, post, stmtGetPostById, post.Id)
	return err
}

func (post *Post) GetPostBySlug(slug string) error {
	err := meddler.QueryRow(db, post, stmtGetPostBySlug, slug)
	return err
}

func (posts *Posts) GetPostsByTag(tagId, page, size int64, onlyPublished bool) (*utils.Pager, error) {
	var (
		pager *utils.Pager
		count int64
	)
	row := db.QueryRow(stmtGetPostsCountByTag, tagId)
	err := row.Scan(&count)
	if err != nil {
		log.Printf("[Error]: ", err.Error())
		return nil, err
	}
	pager = utils.NewPager(page, size, count)

	if !pager.IsValid {
		return pager, fmt.Errorf("Page not found")
	}
	var where string
	if onlyPublished {
		where = "published AND"
	}
	err = meddler.QueryAll(db, posts, fmt.Sprintf(stmtGetPostsByTag, where), tagId, size, pager.Begin)
	return pager, err
}

func (posts *Posts) GetAllPostsByTag(tagId int64) error {
	err := meddler.QueryAll(db, posts, stmtGetAllPostsByTag, tagId)
	return err
}

func GetNumberOfPosts(isPage bool, published bool) (int64, error) {
	var count int64
	var where string
	if isPage {
		where = `page = 1`
	} else {
		where = `page = 0`
	}
	if published {
		where = where + ` AND published`
	}
	var row *sql.Row

	row = db.QueryRow(fmt.Sprintf(stmtNumberOfPosts, where))
	err := row.Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (posts *Posts) GetPostList(page, size int64, isPage bool, onlyPublished bool, orderBy string) (*utils.Pager, error) {
	var pager *utils.Pager
	count, err := GetNumberOfPosts(isPage, onlyPublished)
	pager = utils.NewPager(page, size, count)

	if !pager.IsValid {
		return pager, fmt.Errorf("Page not found")
	}

	var where string
	if isPage {
		where = `page = 1`
	} else {
		where = `page = 0`
	}
	if onlyPublished {
		where = where + ` AND published`
	}
	safeOrderBy := getSafeOrderByStmt(orderBy)
	err = meddler.QueryAll(db, posts, fmt.Sprintf(stmtGetPostList, where, safeOrderBy), size, pager.Begin)
	return pager, err
}

func (posts *Posts) GetAllPostList(isPage bool, onlyPublished bool, orderBy string) error {
	var where string
	if isPage {
		where = `page = 1`
	} else {
		where = `page = 0`
	}
	if onlyPublished {
		where = where + ` AND published`
	}
	safeOrderBy := getSafeOrderByStmt(orderBy)
	err := meddler.QueryAll(db, posts, fmt.Sprintf(stmtGetAllPostList, where, safeOrderBy))
	return err
}

func PostChangeSlug(slug string) bool {
	post := new(Post)
	err := post.GetPostBySlug(slug)
	if err != nil {
		return true
	}
	return false
}

func generateNewSlug(slug string, suffix int) string {
	newSlug := slug + "-" + strconv.Itoa(suffix)
	if !PostChangeSlug(newSlug) {
		return generateNewSlug(slug, suffix+1)
	}
	return newSlug
}

// getSafeOrderByStmt returns a safe `ORDER BY` statement to be when used when
// building SQL queries, in order to prevent SQL injection.
//
// Since we can't use the placeholder `?` to specify the `ORDER BY` values in
// queries, we need to build them using `fmt.Sprintf`. Typically, doing so
// would open you up to SQL injection attacks, since any string can be passed
// into `fmt.Sprintf`, including strings that are valid SQL queries! By using
// this function to check a map of safe values, we guarantee that no unsafe
// values are ever passed to our query building function.
func getSafeOrderByStmt(orderBy string) string {
	if stmt, ok := safeOrderByStmt[orderBy]; ok {
		return stmt
	}
	return "published_at DESC"
}

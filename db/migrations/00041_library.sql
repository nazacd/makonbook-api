-- +goose Up
-- The Library (spec 25): SAT MAKON's own books, read in the apps. Imported
-- from the private makonbook-library repository with `makonbook library
-- import`, never committed here; the PDFs and covers are in MakonBook's R2
-- bucket (pdf_key, cover_key), served by the API.
CREATE TABLE library_books (
    id            text PRIMARY KEY CHECK (id ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    title         text NOT NULL,
    author        text NOT NULL,
    subject       text NOT NULL CHECK (subject IN ('math', 'english')),
    description   text NOT NULL,
    pages         integer NOT NULL CHECK (pages > 0),
    pdf_key       text NOT NULL,       -- library/books/<sha256>.pdf
    pdf_sha256    text NOT NULL,
    pdf_bytes     integer NOT NULL CHECK (pdf_bytes > 0),
    cover_key     text,                -- library/covers/<sha256>.<ext>
    cover_type    text CHECK (cover_type IN ('image/png', 'image/jpeg', 'image/webp')),
    cover_sha256  text,
    listed        boolean NOT NULL DEFAULT true,  -- false: gone from the data; kept with its readers' progress
    position      integer NOT NULL DEFAULT 0,     -- the order of the files' names
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((cover_key IS NULL) = (cover_type IS NULL) AND (cover_key IS NULL) = (cover_sha256 IS NULL))
);

-- Where each reader is in each book. A row means the person has opened the
-- book, so a book's views are its rows. No foreign key to people (students
-- and teachers are two tables); an account merge moves or deletes the rows.
CREATE TABLE library_progress (
    person_kind  text NOT NULL CHECK (person_kind IN ('student', 'teacher')),
    person_id    integer NOT NULL,
    book_id      text NOT NULL REFERENCES library_books ON DELETE CASCADE ON UPDATE CASCADE,
    page         integer NOT NULL CHECK (page >= 1),
    opened_at    timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (person_kind, person_id, book_id)
);
CREATE INDEX library_progress_book ON library_progress (book_id);

-- +goose Down
DROP TABLE library_progress;
DROP TABLE library_books;

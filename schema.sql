CREATE TABLE "public"."items" (
    "id" bigserial NOT NULL,
    "name" text NOT NULL,
    "category" text NOT NULL DEFAULT ''::text,
    "price" numeric NOT NULL,
    "url" text NOT NULL,
    "image_url" text NOT NULL,
    "created_at" timestamptz NOT NULL,
    "updated_at" timestamptz NOT NULL,
    PRIMARY KEY ("id")
);

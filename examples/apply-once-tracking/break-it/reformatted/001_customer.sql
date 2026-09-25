-- customer: one row per account holder
create table app.customer (
    id int primary key,   -- stable key
    name text not null
);

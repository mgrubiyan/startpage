-- What the markets panel shows, in display order. The defaults are what it
-- showed while the lists were fixed in code.
ALTER TABLE settings
    ADD COLUMN crypto text[] NOT NULL DEFAULT '{BTC,ETH,SOL,TON}'
        CHECK (cardinality(crypto) <= 8),
    ADD COLUMN shares text[] NOT NULL DEFAULT '{SBER,GAZP,LKOH,YDEX}'
        CHECK (cardinality(shares) <= 8);

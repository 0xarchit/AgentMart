-- Points every seeded product at the illustration for its category. The list of
-- categories is written out rather than derived, so a category added later with
-- no artwork behind it gets a null and the storefront draws its own placeholder
-- instead of a broken image.
--
-- These are drawn line illustrations served from the site itself, not
-- photographs. The products in this catalog are invented, so a real photograph
-- would either show a different maker's goods or point at a host that can stop
-- serving it. Both read worse than a clean illustration on the shop's own palette.
update public.products
set image_url = '/products/' || category || '.svg'
where
    image_url is null
    and category in (
        'trimmer',
        'shaver',
        'cream',
        'beard_oil',
        'face_wash',
        'hair_dryer',
        'serum',
        'straightener'
    );

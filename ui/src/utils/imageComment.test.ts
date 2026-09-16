import {
  buildImageCommentBlocks,
  displayNeedsAutoOrient,
  displaySourceSize,
  imageCommentHeader,
  imageRefFromSrc,
  regionGeometry,
  regionIn,
  type ImageBox,
} from "./imageComment";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}

function eq(got: unknown, want: unknown, msg: string): void {
  assert(got === want, `${msg}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

/** The middle half of an image: a natural stand-in for a user's drag. */
const MIDDLE: ImageBox = { left: 0.25, top: 0.25, right: 0.75, bottom: 0.75 };

run("an explicit path wins over the src", () => {
  eq(imageRefFromSrc("/api/message/m1/image/1/-1", "/tmp/shot.png"), "/tmp/shot.png", "path");
});

run("recovers the path from a file-endpoint src", () => {
  eq(
    imageRefFromSrc("/api/message/m1/file?path=%2Ftmp%2Fdemo%20dir%2Fa.png"),
    "/tmp/demo dir/a.png",
    "file endpoint",
  );
  eq(imageRefFromSrc("/api/read?path=%2Ftmp%2Fshot.png"), "/tmp/shot.png", "read endpoint");
});

run("falls back to the src when there is no path", () => {
  eq(imageRefFromSrc("/api/message/m1/image/1/-1"), "/api/message/m1/image/1/-1", "no path");
});

run("does not mistake a remote URL's query for a local path", () => {
  const remote = "https://elsewhere.example/pic?path=/etc/passwd";
  eq(imageRefFromSrc(remote), remote, "remote");
  // Nor a same-origin URL outside the API, which serves no local files.
  eq(imageRefFromSrc("/assets/x.png?path=/etc/passwd"), "/assets/x.png?path=/etc/passwd", "asset");
});

run("a fragment does not leak into the recovered path", () => {
  eq(imageRefFromSrc("/api/read?path=%2Ftmp%2Fa.png#frag"), "/tmp/a.png", "fragment");
});

run("names data URIs instead of quoting them", () => {
  eq(imageRefFromSrc("data:image/png;base64,AAAA"), "(inline image)", "data uri");
});

run("region geometry is ImageMagick-shaped", () => {
  eq(regionGeometry({ x: 120, y: 340, width: 300, height: 180 }), "300x180+120+340", "geometry");
});

run("a box resolves to whole pixels of the given size", () => {
  eq(regionGeometry(regionIn(MIDDLE, { width: 48, height: 48 })), "24x24+12+12", "small");
  // The same box against a larger source: no compounding rounding error, which
  // is the whole reason boxes are stored as fractions rather than pixels.
  eq(regionGeometry(regionIn(MIDDLE, { width: 3000, height: 1000 })), "1500x500+750+250", "large");
});

run("a resolved region never leaves the image", () => {
  const r = regionIn({ left: 0.99, top: 0.995, right: 1, bottom: 1 }, { width: 33, height: 33 });
  assert(r.x + r.width <= 33, `x+width ${r.x + r.width} within 33`);
  assert(r.y + r.height <= 33, `y+height ${r.y + r.height} within 33`);
  assert(r.width >= 1 && r.height >= 1, "a region never collapses to nothing");
});

run("headers describe regions and whole images", () => {
  eq(
    imageCommentHeader(
      "/tmp/a.png",
      { width: 1280, height: 800 },
      { x: 1, y: 2, width: 3, height: 4 },
    ),
    "image /tmp/a.png [region 3x4+1+2 of 1280x800]",
    "region header",
  );
  eq(
    imageCommentHeader("/tmp/a.png", { width: 1280, height: 800 }, undefined),
    "image /tmp/a.png [whole image, 1280x800]",
    "whole-image header",
  );
});

run("builds one quoted block per annotation", () => {
  const out = buildImageCommentBlocks("/tmp/a.png", { width: 48, height: 48 }, [
    { box: MIDDLE, text: "first" },
    { text: "  second  " },
  ]);
  eq(
    out,
    "> image /tmp/a.png [region 24x24+12+12 of 48x48]\nfirst\n\n" +
      "> image /tmp/a.png [whole image, 48x48]\nsecond\n\n",
    "blocks",
  );
});

run("drops empty annotations", () => {
  eq(
    buildImageCommentBlocks("/tmp/a.png", { width: 1, height: 1 }, [{ text: "   " }]),
    "",
    "empty",
  );
  eq(buildImageCommentBlocks("/tmp/a.png", { width: 1, height: 1 }, []), "", "none");
});

run("keeps multi-line comment text intact", () => {
  eq(
    buildImageCommentBlocks("/tmp/a.png", { width: 1, height: 1 }, [{ text: "one\ntwo" }]),
    "> image /tmp/a.png [whole image, 1x1]\none\ntwo\n\n",
    "multiline",
  );
});

run("source size is read from a tool's display data", () => {
  eq(
    JSON.stringify(displaySourceSize({ source_width: 1280, source_height: 800 })),
    JSON.stringify({ width: 1280, height: 800 }),
    "present",
  );
  eq(displaySourceSize({ source_width: 1280 }), undefined, "half-present");
  eq(displaySourceSize({ source_width: 0, source_height: 0 }), undefined, "zero");
  eq(displaySourceSize({ path: "/tmp/a.png" }), undefined, "absent");
  eq(displaySourceSize(undefined), undefined, "no display");
});

run("an EXIF-rotated source tells the reader to auto-orient before cropping", () => {
  // Viewers apply the orientation tag and croppers do not, so a region measured
  // against what the user saw needs the instruction to come with it.
  eq(
    buildImageCommentBlocks("/tmp/a.jpg", { width: 50, height: 100 }, [{ text: "here" }], true),
    "> image /tmp/a.jpg [whole image, 50x100, auto-orient first]\nhere\n\n",
    "whole image",
  );
  eq(
    buildImageCommentBlocks(
      "/tmp/a.jpg",
      { width: 50, height: 100 },
      [{ box: { left: 0, top: 0, right: 0.5, bottom: 0.5 }, text: "here" }],
      true,
    ),
    "> image /tmp/a.jpg [region 25x50+0+0 of 50x100, auto-orient first]\nhere\n\n",
    "region",
  );
  // Absent for the overwhelming majority of images, where it would be noise.
  eq(
    buildImageCommentBlocks("/tmp/a.png", { width: 8, height: 8 }, [{ text: "here" }], false),
    "> image /tmp/a.png [whole image, 8x8]\nhere\n\n",
    "unrotated",
  );

  eq(displayNeedsAutoOrient({ source_orientation: 6 }), true, "rotated");
  eq(displayNeedsAutoOrient({ source_orientation: 1 }), false, "normal");
  eq(displayNeedsAutoOrient({ path: "/tmp/a.png" }), false, "absent");
  eq(displayNeedsAutoOrient(undefined), false, "no display");
});

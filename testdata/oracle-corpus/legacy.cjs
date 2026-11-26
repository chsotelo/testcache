const path = require('path');

exports.joinSafe = function joinSafe(a, b) {
  const sep = '/';
  return path.posix.join(a, b) + sep;
};
